package common

import (
	"bytes"
	"fmt"
	"github.com/caarlos0/env"
	"github.com/devtron-labs/source-controller/bean"
	repository "github.com/devtron-labs/source-controller/sql/repo"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/util/json"
	"net/http"
)

type CommonService interface {
	FilterAlreadyPresentArtifacts(imageDigests []string, digestTagMap map[string]string, externalCiId int) error
	CallExternalCIWebHook(digest, tag, host, repoName string, externalCiId int) error
}

type CommonServiceImpl struct {
	logger               *zap.SugaredLogger
	ciArtifactRepository repository.CiArtifactRepository
	config               *CommonServiceConfig
}

type CommonServiceConfig struct {
	ApiToken    string `env:"API_TOKEN_EXTERNAL_CI" envDefault:""`
	ServiceName string `env:"WEBHOOK_SERVICE_NAME" envDefault:"devtron-service"`
	Namespace   string `env:"WEBHOOK_NAMESPACE" envDefault:"devtroncd"`
}

func NewCommonServiceImpl(logger *zap.SugaredLogger,
	ciArtifactRepository repository.CiArtifactRepository) *CommonServiceImpl {
	cfg := &CommonServiceConfig{}
	err := env.Parse(cfg)
	if err != nil {
		fmt.Println("failed to parse server cluster status config: " + err.Error())
	}
	sourceControllerServiceImpl := &CommonServiceImpl{
		logger:               logger,
		config:               cfg,
		ciArtifactRepository: ciArtifactRepository,
	}

	return sourceControllerServiceImpl
}

func (impl *CommonServiceImpl) FilterAlreadyPresentArtifacts(imageDigests []string, digestTagMap map[string]string, externalCiId int) error {
	ciArtifacts, err := impl.ciArtifactRepository.GetByImageDigests(imageDigests, externalCiId)
	if err != nil {
		impl.logger.Errorw("error in getting ci artifact by image digests ", "err", err)
		return err
	}
	for _, ciArtifact := range ciArtifacts {
		delete(digestTagMap, ciArtifact.ImageDigest)
	}
	return nil

}

// CallExternalCIWebHook will do a http post request using service name and namespace on which orchestrator is running
func (impl *CommonServiceImpl) CallExternalCIWebHook(digest, tag, host, repoName string, externalCiId int) error {
	image := bean.ParseImage(host, repoName, tag)
	url := bean.GetParsedWebhookServiceURL(impl.config.ServiceName, impl.config.Namespace, externalCiId)
	payload := bean.GetPayloadForExternalCi(image, digest)
	b, err := json.Marshal(payload)
	if err != nil {
		impl.logger.Errorw("error in marshalling golang struct", "err", err)
		return err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(b))
	if err != nil {
		impl.logger.Errorw("error in new http POST request", "err", err)
		return err
	}
	impl.logger.Infow("cron request", req)
	req.Header.Set("api-token", impl.config.ApiToken)
	req.Header.Add("Content-Type", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		impl.logger.Errorw("error in hitting http request to web hook", "err", err)
		return err
	}
	defer resp.Body.Close()

	return nil
}
