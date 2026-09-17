package fabrica

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// JobIDLength e o tamanho (em caracteres hexadecimais) dos ids de cron job
// gerados pela Fabrica. O Hermes grava ids de 12 caracteres em cron/jobs.json
// (ex.: "e054c4e9d970") e a API usa o mesmo formato para que um job criado
// pelo dashboard seja indistinguivel de um criado pela CLI.
const JobIDLength = 12

// NewJobID gera um identificador de cron job unico no formato do Hermes.
func NewJobID() (string, error) {
	buf := make([]byte, JobIDLength/2)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("fabrica: gerar id de cron job: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
