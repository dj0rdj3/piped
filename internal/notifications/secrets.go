package notifications

import (
	"errors"
	"strings"
	"unicode/utf8"

	"piped/internal/store"
)

type SecretInput struct {
	Name  string  `json:"name"`
	Value *string `json:"value,omitempty"`
}

func reservedSecretName(name string) bool {
	switch strings.ToLower(name) {
	case "host", "job", "status", "output":
		return true
	default:
		return false
	}
}

// Template names are ASCII identifiers; folding never changes literal values.
func lookupSecret(values map[string]string, name string) (string, bool) {
	var result string
	found := false
	for key, value := range values {
		if strings.EqualFold(key, name) {
			if found {
				return "", false
			}
			result, found = value, true
		}
	}
	return result, found
}

func validateSecretInputs(input []SecretInput) error {
	if len(input) > 16 {
		return invalid("use at most 16 notification secrets")
	}
	seen := map[string]bool{}
	for _, secret := range input {
		if len(secret.Name) < 1 || len(secret.Name) > 64 || templateNameEnd(secret.Name, 0) != len(secret.Name) || secret.Name[0] >= '0' && secret.Name[0] <= '9' {
			return invalid("secret names must use 1-64 letters, digits, or underscores and start with a letter or underscore")
		}
		name := strings.ToLower(secret.Name)
		if reservedSecretName(name) {
			return invalid("secret names cannot be host, job, status, or output, regardless of case")
		}
		if seen[name] {
			return invalid("notification secret names must be unique ignoring case")
		}
		seen[name] = true
		if secret.Value != nil && (!utf8.ValidString(*secret.Value) || len(*secret.Value) < 1 || len(*secret.Value) > 4096 || strings.ContainsRune(*secret.Value, 0)) {
			return invalid("secret values must contain 1-4096 bytes of text without null characters")
		}
	}
	return nil
}

func (s *Service) encryptSecretChanges(id string, input []SecretInput) []store.NotificationSecret {
	if input == nil {
		return nil
	}
	values := make([]store.NotificationSecret, 0, len(input))
	for _, secret := range input {
		value := store.NotificationSecret{Name: secret.Name}
		if secret.Value != nil {
			plain := []byte(*secret.Value)
			value.Ciphertext = s.vault.Seal("notification:"+id+":secret:"+secret.Name, plain)
			clear(plain)
		}
		values = append(values, value)
	}
	return values
}

func (s *Service) secretValues(target store.NotificationTarget) (map[string]string, error) {
	values := map[string]string{}
	for _, secret := range target.Secrets {
		name := strings.ToLower(secret.Name)
		if reservedSecretName(name) {
			clear(values)
			return nil, invalid("a stored secret conflicts with a built-in template name; rename or remove it")
		}
		if _, exists := values[name]; exists {
			clear(values)
			return nil, invalid("stored secret names conflict ignoring case; rename or remove duplicates")
		}
		plain, err := s.vault.Open("notification:"+target.ID+":secret:"+secret.Name, secret.Ciphertext)
		if err != nil {
			clear(values)
			return nil, errors.New("cannot decrypt notification secret")
		}
		values[name] = string(plain)
		clear(plain)
	}
	return values, nil
}
