//go:build wireinject
// +build wireinject

package main

import (
	"github.com/devtron-labs/source-controller/api"
	"github.com/devtron-labs/source-controller/internal/logger"
	"github.com/devtron-labs/source-controller/sql"
	repository "github.com/devtron-labs/source-controller/sql/repo"
	"github.com/google/wire"
)

func InitializeApp() (*App, error) {
	wire.Build(
		NewApp,
		logger.NewSugardLogger,
		api.NewRouter,
		sql.GetConfig,
		sql.NewDbConnection,
		GetSourceControllerConfig,

		repository.NewCiArtifactRepositoryImpl,
		wire.Bind(new(repository.CiArtifactRepository), new(*repository.CiArtifactRepositoryImpl)),

		NewSourceControllerServiceImpl,
		wire.Bind(new(SourceControllerService), new(*SourceControllerServiceImpl)),

		//NewSourceControllerCronServiceImpl,
		//wire.Bind(new(SourceControllerCronService), new(*SourceControllerCronServiceImpl)),
	)
	return &App{}, nil
}
