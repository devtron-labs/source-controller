package ecr

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/caarlos0/env"
	common2 "github.com/devtron-labs/source-controller/common"
	repository "github.com/devtron-labs/source-controller/sql/repo"
	"go.uber.org/zap"
)

type ReconciliationEcrService interface {
	GetAwsClientFromCred(s3BaseConfig *AwsS3BaseConfig) (*ecr.Client, error)
	GetAllImagesList(client *ecr.Client, registryId, repositoryName string, externalCiPipelineId int, hostUrl string) error
}

type ReconciliationEcrServiceImpl struct {
	logger               *zap.SugaredLogger
	ciArtifactRepository repository.CiArtifactRepository
	commonService        common2.CommonService
	config               *ReconciliationConfig
}

func NewReconciliationServiceImpl(logger *zap.SugaredLogger,
	ciArtifactRepository repository.CiArtifactRepository,
	commonService common2.CommonService) *ReconciliationEcrServiceImpl {
	cfg := &ReconciliationConfig{}
	err := env.Parse(cfg)
	if err != nil {
		fmt.Println("failed to parse server cluster status config: " + err.Error())
	}
	reconciliationEcrServiceImpl := &ReconciliationEcrServiceImpl{
		logger:               logger,
		ciArtifactRepository: ciArtifactRepository,
		commonService:        commonService,
		config:               cfg,
	}
	return reconciliationEcrServiceImpl
}

type ReconciliationConfig struct {
	ImageShowCount int `env:"IMAGE_COUNT_FROM_REPO" envDefault:"20"`
}
type AwsS3BaseConfig struct {
	AccessKey   string `json:"accessKey"`
	Passkey     string `json:"passkey"`
	EndpointUrl string `json:"endpointUrl"`
	IsInSecure  bool   `json:"isInSecure"`
	Region      string `json:"region"`
}

type ImageIdentifier struct {
	Image    string `json:"image"`
	ImageTag string `json:"imageTag"`
}

func (impl *ReconciliationEcrServiceImpl) GetAwsClientFromCred(s3BaseConfig *AwsS3BaseConfig) (*ecr.Client, error) {
	cfg, err := config.LoadDefaultConfig(context.Background(), config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(s3BaseConfig.AccessKey, s3BaseConfig.Passkey, "")))
	if err != nil {
		impl.logger.Errorw("error in loading default config from aws ecr credentials", "err", err)
		return nil, err
	}
	cfg.Region = s3BaseConfig.Region
	// Create ECR client from Config
	svcClient := ecr.NewFromConfig(cfg)

	return svcClient, err
}

func (impl *ReconciliationEcrServiceImpl) GetAllImagesList(client *ecr.Client, registryId, repositoryName string, externalCiPipelineId int, hostUrl string) error {
	// TODO: Move Host url in another service
	hostUrl = getHostUrlForEcr(registryId, "region")
	// TODO: move it
	listImageInput := &ecr.ListImagesInput{
		RepositoryName: &repositoryName,
		RegistryId:     &registryId,
	}
	listImagesOutput, err := client.ListImages(context.Background(), listImageInput)
	if err != nil {
		impl.logger.Errorw("error in list images from ecr", "err", err, "repoName", repositoryName, "registryId", registryId)
		return err
	}
	imageIdsIdentifier := listImagesOutput.ImageIds
	digestTagMap, err := impl.filterImages(imageIdsIdentifier, externalCiPipelineId)
	if err != nil {
		impl.logger.Errorw("error in filtering images", "err", err)
		return err
	}

	for digest, tag := range digestTagMap {
		err = impl.commonService.CallExternalCIWebHook(digest, tag, hostUrl, repositoryName, externalCiPipelineId)
		if err != nil {
			impl.logger.Errorw("error in calling external ci webhook", "err", err, "digest", digest, "repoName", repositoryName, "externalCiId", externalCiPipelineId)
		}
	}

	return nil
}

// /445808685819.dkr.ecr.us-east-2.amazonaws.com/devtron/html-ecr:cf50e450-125-588///Sample Image for reference
func getHostUrlForEcr(registryId, region string) string {
	return fmt.Sprintf("%s.dkr.ecr.%s.amazonaws.com", registryId, region)
}

func (impl *ReconciliationEcrServiceImpl) filterImages(imageIdsIdentifier []types.ImageIdentifier, externalCiPipelineId int) (map[string]string, error) {
	digests := make([]string, 0, len(imageIdsIdentifier))
	digestTagMap := make(map[string]string)
	for i := 0; i < len(imageIdsIdentifier) && i < impl.config.ImageShowCount; i++ {
		digests = append(digests, *imageIdsIdentifier[i].ImageDigest)
		digestTagMap[*imageIdsIdentifier[i].ImageDigest] = *imageIdsIdentifier[i].ImageTag
	}
	err := impl.commonService.FilterAlreadyPresentArtifacts(digests, digestTagMap, externalCiPipelineId)
	if err != nil {
		impl.logger.Errorw("error in filtering already present artifacts", "err", err, "digests", digests)
		return nil, err
	}
	return digestTagMap, err
}
