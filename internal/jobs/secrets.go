package jobs

import (
	"strings"
	"unicode/utf8"

	"piped/internal/secrets"
	"piped/internal/store"
)

const MaxSecrets = 16
const MaxSecretBytes = 4096
const MaxTotalSecretBytes = 64 * 1024

type SecretInput struct {
	Name  string  `json:"name"`
	Value *string `json:"value,omitempty"`
}

func validateSecrets(input []SecretInput) error {
	if len(input) > MaxSecrets {
		return invalid("use at most 16 job secrets")
	}
	seen := make(map[string]bool, len(input))
	total := 0
	for _, secret := range input {
		if !secrets.ValidVariableName(secret.Name) {
			return invalid("secret names must use uppercase letters, digits, and underscores, start with a letter or underscore, and not override shell or runtime settings")
		}
		if seen[secret.Name] {
			return invalid("job secret names must be unique")
		}
		seen[secret.Name] = true
		if secret.Value != nil {
			value := *secret.Value
			if !utf8.ValidString(value) || len(value) == 0 || len(value) > MaxSecretBytes || strings.ContainsRune(value, 0) {
				return invalid("secret values must contain 1-4096 bytes of text without null characters")
			}
			total += len(value)
		}
	}
	if total > MaxTotalSecretBytes {
		return invalid("job secret values must total at most 64 KiB")
	}
	return nil
}

func (s *Service) encryptSecrets(jobID string, input []SecretInput) []store.JobSecret {
	if input == nil {
		return nil
	}
	encrypted := make([]store.JobSecret, 0, len(input))
	for _, secret := range input {
		item := store.JobSecret{Name: secret.Name}
		if secret.Value != nil {
			plain := []byte(*secret.Value)
			item.Ciphertext = s.vault.Seal("job:"+jobID+":env:"+secret.Name, plain)
			clear(plain)
		}
		encrypted = append(encrypted, item)
	}
	return encrypted
}
