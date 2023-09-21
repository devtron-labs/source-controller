package main

import (
	"context"
	"fmt"
	"github.com/caarlos0/env"
	"github.com/devtron-labs/source-controller/bean"
	"github.com/devtron-labs/source-controller/common"
	"github.com/devtron-labs/source-controller/oci"
	repository "github.com/devtron-labs/source-controller/sql/repo"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	gcrv1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"go.uber.org/zap"
	"gopkg.in/yaml.v2"
	kuberecorder "k8s.io/client-go/tools/record"
	"net/http"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
)

type SourceControllerService interface {
	ReconcileSource(ctx context.Context, deployConfig DeployConfig) (bean.Result, error)
	ReconcileSourceWrapper()
}

type SourceControllerServiceImpl struct {
	logger               *zap.SugaredLogger
	SCSconfig            *SourceControllerConfig
	ciArtifactRepository repository.CiArtifactRepository
	commonService        common.CommonService
	client.Client
	kuberecorder.EventRecorder
}

type SourceControllerConfig struct {
	ImageShowCount            int    `env:"IMAGE_COUNT_FROM_REPO" envDefault:"20"`
	Insecure                  bool   `env:"INSECURE_EXTERNAL_CI" envDefault:"true"`
	ApiToken                  string `env:"API_TOKEN_EXTERNAL_CI" envDefault:""`
	ServiceName               string `env:"WEBHOOK_SERVICE_NAME" envDefault:"devtron-service"`
	Namespace                 string `env:"WEBHOOK_NAMESPACE" envDefault:"devtroncd"`
	DeployConfigExternalCi    string `env:"DEPLOY_CONFIG_EXTERNAL_CI"`
	DeployConfigExternalCiObj []DeployConfig
}

type DeployConfig struct {
	ExternalCiId int    `yaml:"EXTERNAL_CI_ID"`
	RepoName     string `yaml:"REPO_NAME_EXTERNAL_CI"`
	RegistryURL  string `yaml:"REGISTRY_URL_EXTERNAL_CI"`
}

var UserAgent = "flux/v2"

type invalidOCIURLError struct {
	err error
}

func (e invalidOCIURLError) Error() string {
	return e.err.Error()
}

func NewSourceControllerServiceImpl(logger *zap.SugaredLogger,
	cfg *SourceControllerConfig,
	ciArtifactRepository repository.CiArtifactRepository) *SourceControllerServiceImpl {
	sourceControllerServiceImpl := &SourceControllerServiceImpl{
		logger:               logger,
		SCSconfig:            cfg,
		ciArtifactRepository: ciArtifactRepository,
	}

	return sourceControllerServiceImpl
}

func GetSourceControllerConfig() (*SourceControllerConfig, error) {
	cfg := &SourceControllerConfig{}
	err := env.Parse(cfg)
	if err != nil {
		fmt.Println("failed to parse server cluster status config: " + err.Error())
		return nil, err
	}
	deployConfig, err := UnmarshalDeployConfig(cfg.DeployConfigExternalCi)
	if err != nil {
		fmt.Println("error in unmarshalling deploy config", "err", err)
		return nil, err
	}
	cfg.DeployConfigExternalCiObj = deployConfig
	return cfg, err
}

// Have Kept For reference (can be used in future)
//type CiCompleteEvent struct {
//	CiProjectDetails   []pipeline.CiProjectDetails `json:"ciProjectDetails"`
//	DockerImage        string                      `json:"dockerImage" validate:"required,image-validator"`
//	Digest             string                      `json:"digest"`
//	PipelineId         int                         `json:"pipelineId"`
//	WorkflowId         *int                        `json:"workflowId"`
//	TriggeredBy        int32                       `json:"triggeredBy"`
//	PipelineName       string                      `json:"pipelineName"`
//	DataSource         string                      `json:"dataSource"`
//	MaterialType       string                      `json:"materialType"`
//	Metrics            util.CIMetrics              `json:"metrics"`
//	AppName            string                      `json:"appName"`
//	IsArtifactUploaded bool                        `json:"isArtifactUploaded"`
//	FailureReason      string                      `json:"failureReason"`
//}

func (impl *SourceControllerServiceImpl) ReconcileSourceWrapper() {
	fmt.Println("cron started")
	deployConfig := impl.SCSconfig.DeployConfigExternalCiObj
	impl.logger.Infow("deploy config after unmarshalling yaml", "deployConfig", deployConfig)
	if len(deployConfig) == 0 {
		impl.logger.Errorw("error: no deploy config provided")
		return
	}
	for i := 0; i < len(deployConfig); i++ {
		result, err := impl.ReconcileSource(context.Background(), deployConfig[i])
		if err != nil {
			impl.logger.Errorw("error in reconciling sources", "err", err, "result", result)

		}
	}
	fmt.Println("cron ended")
}

func (impl *SourceControllerServiceImpl) ReconcileSource(ctx context.Context, deployConfig DeployConfig) (bean.Result, error) {
	var auth authn.Authenticator
	keychain := oci.Anonymous{}
	transport := remote.DefaultTransport.(*http.Transport).Clone()
	opts := makeRemoteOptions(ctx, transport, keychain, auth, impl.SCSconfig.Insecure)

	url, err := parseRepositoryURLInValidFormat(deployConfig.RegistryURL, deployConfig.RepoName)
	if err != nil {
		impl.logger.Errorw("error in parsing repository url in valid format", "err", err)
		return bean.ResultEmpty, invalidOCIURLError{err}
	}
	tags, err := getAllTags(url, opts.craneOpts)
	if err != nil {
		impl.logger.Errorw("error in getting all tags ", "err", err, "url", url)
		return bean.ResultEmpty, err
	}
	digests := make([]string, 0, len(tags))
	digestTagMap := make(map[string]string)
	for i := 0; i < len(tags) && i < impl.SCSconfig.ImageShowCount; i++ {
		tag := tags[i]
		// Determine which artifact revision to pull
		tagUrl, err := getArtifactURLForTag(url, tag)
		if err != nil {
			impl.logger.Errorw("error in getting artifact url", "err", err, "tag", tag)
			return bean.ResultEmpty, err
		}
		digest, err := crane.Digest(tagUrl, opts.craneOpts...)
		if err != nil {
			fmt.Errorf("error")

		}
		digestTagMap[digest] = tag
		digests = append(digests, digest)
	}

	err = impl.commonService.FilterAlreadyPresentArtifacts(digests, digestTagMap, deployConfig.ExternalCiId)
	if err != nil {
		impl.logger.Errorw("error in filtering artifacts", "err", err)
		return bean.ResultEmpty, err
	}
	for digest, tag := range digestTagMap {
		err = impl.commonService.CallExternalCIWebHook(digest, tag, deployConfig.RegistryURL, deployConfig.RepoName, deployConfig.ExternalCiId)
		if err != nil {
			impl.logger.Errorw("error in calling external ci webhook", "err", err, "digest", digest, "repoName", deployConfig.RepoName, "externalCiId", deployConfig.ExternalCiId)
		}
	}
	return bean.ResultSuccess, err
}

func UnmarshalDeployConfig(data string) ([]DeployConfig, error) {
	var deployConfig []DeployConfig
	err := yaml.Unmarshal([]byte(data), &deployConfig)
	if err != nil {
		return nil, err
	}
	return deployConfig, nil
}

// getAllTags call the remote container registry, fetches all the tags from the repository
func getAllTags(url string, options []crane.Option) ([]string, error) {
	tags, err := crane.ListTags(url, options...)
	if err != nil {
		return nil, err
	}
	return tags, nil
}

// getArtifactURL determines which tag or revision should be used and returns the OCI artifact FQN.
func getArtifactURLForTag(tag, url string) (string, error) {
	if tag != "" {
		return fmt.Sprintf("%s:%s", tag, url), nil
	}
	return url, nil
}

// parseRepositoryURL validates and extracts the repository URL.
func parseRepositoryURLInValidFormat(registryUrl, repo string) (string, error) {
	url := fmt.Sprintf("%s/%s", registryUrl, repo)
	ref, err := name.ParseReference(url)
	if err != nil {
		return "", err
	}

	imageName := strings.TrimPrefix(url, ref.Context().RegistryStr())
	if s := strings.Split(imageName, ":"); len(s) > 1 {
		return "", fmt.Errorf("URL must not contain a tag; remove ':%s'", s[1])
	}
	return ref.Context().Name(), nil
}

// getRevision fetches the upstream digest, returning the revision in the
// format '<tag>@<digest>'.
func (r *SourceControllerServiceImpl) getRevision(url string, options []crane.Option) (string, error) {
	ref, err := name.ParseReference(url)
	if err != nil {
		return "", err
	}

	repoTag := ""
	repoName := strings.TrimPrefix(url, ref.Context().RegistryStr())
	if s := strings.Split(repoName, ":"); len(s) == 2 && !strings.Contains(repoName, "@") {
		repoTag = s[1]
	}

	if repoTag == "" && !strings.Contains(repoName, "@") {
		repoTag = "latest"
	}

	digest, err := crane.Digest(url, options...)
	if err != nil {
		return "", err
	}

	digestHash, err := gcrv1.NewHash(digest)
	if err != nil {
		return "", err
	}

	revision := digestHash.String()
	if repoTag != "" {
		revision = fmt.Sprintf("%s@%s", repoTag, revision)
	}
	return revision, nil
}

// remoteOptions contains the options to interact with a remote registry.
// It can be used to pass options to go-containerregistry based libraries.
type remoteOptions struct {
	craneOpts  []crane.Option
	verifyOpts []remote.Option
}

// makeRemoteOptions returns a remoteOptions struct with the authentication and transport options set.
// The returned struct can be used to interact with a remote registry using go-containerregistry based libraries.
func makeRemoteOptions(ctxTimeout context.Context, transport http.RoundTripper, keychain authn.Keychain, auth authn.Authenticator, insecure bool) remoteOptions {
	// have to make it configurable insecure in future iterations
	o := remoteOptions{
		craneOpts:  craneOptions(ctxTimeout, insecure),
		verifyOpts: []remote.Option{},
	}

	if transport != nil {
		o.craneOpts = append(o.craneOpts, crane.WithTransport(transport))
		o.verifyOpts = append(o.verifyOpts, remote.WithTransport(transport))
	}

	if auth != nil {
		// auth take precedence over keychain here as we expect the caller to set
		// the auth only if it is required.
		o.verifyOpts = append(o.verifyOpts, remote.WithAuth(auth))
		o.craneOpts = append(o.craneOpts, crane.WithAuth(auth))
		return o
	}

	o.verifyOpts = append(o.verifyOpts, remote.WithAuthFromKeychain(keychain))
	o.craneOpts = append(o.craneOpts, crane.WithAuthFromKeychain(keychain))

	return o
}

// craneOptions sets the auth headers, timeout and user agent
// for all operations against remote container registries.
func craneOptions(ctx context.Context, insecure bool) []crane.Option {
	options := []crane.Option{
		crane.WithContext(ctx),
		crane.WithUserAgent(UserAgent),
	}

	if insecure {
		options = append(options, crane.Insecure)
	}

	return options
}
