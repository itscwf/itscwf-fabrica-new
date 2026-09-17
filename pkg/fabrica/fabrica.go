// Package fabrica expoe identidade, constantes e metadados de build da
// plataforma Fabrica ITSCWF. E importavel por qualquer modulo do repositorio
// (inclusive ferramentas fora do cmd/server).
package fabrica

import "runtime"

// Identidade do servico.
const (
	Name        = "itscwf-fabrica"
	DisplayName = "Fabrica ITSCWF"
	APIPrefix   = "/api/v1"
)

// Versao do schema/API exposta em /health.
const APIVersion = "v1"

// Version e Commit sao sobrescritos em tempo de build:
//
//	go build -ldflags "-X github.com/itscwf/itscwf-fabrica-new/pkg/fabrica.Version=v0.1.0"
var (
	Version = "dev"
	Commit  = "unknown"
)

// Info descreve a build em execucao.
type Info struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	GoVersion string `json:"go_version"`
}

// BuildInfo devolve os metadados da build atual.
func BuildInfo() Info {
	return Info{
		Name:      Name,
		Version:   Version,
		Commit:    Commit,
		GoVersion: runtime.Version(),
	}
}
