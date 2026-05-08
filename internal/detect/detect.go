package detect

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type DetectResult struct {
	OdooVersion   string
	OdooContainer string
	DBContainer   string
	DBName        string
}

type composeFile struct {
	Services map[string]composeService `yaml:"services"`
}

type composeService struct {
	Image       string   `yaml:"image"`
	Environment yaml.Node `yaml:"environment"`
}

func FromCompose(dir string) DetectResult {
	data, err := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if err != nil {
		data, err = os.ReadFile(filepath.Join(dir, "docker-compose.yaml"))
		if err != nil {
			return DetectResult{}
		}
	}

	var cf composeFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return DetectResult{}
	}

	var result DetectResult
	for name, svc := range cf.Services {
		img := strings.ToLower(svc.Image)
		if strings.HasPrefix(img, "odoo:") {
			result.OdooContainer = name
			result.OdooVersion = parseOdooVersion(img)
		}
		if strings.HasPrefix(img, "postgres:") {
			result.DBContainer = name
			result.DBName = extractPostgresDB(svc.Environment)
		}
	}
	return result
}

func parseOdooVersion(image string) string {
	tag := strings.TrimPrefix(image, "odoo:")
	tag = strings.TrimSuffix(tag, ".0")
	if tag == "latest" || tag == "" {
		return ""
	}
	return tag
}

func extractPostgresDB(node yaml.Node) string {
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == "POSTGRES_DB" {
				return node.Content[i+1].Value
			}
		}
	}
	if node.Kind == yaml.SequenceNode {
		for _, item := range node.Content {
			if strings.HasPrefix(item.Value, "POSTGRES_DB=") {
				return strings.TrimPrefix(item.Value, "POSTGRES_DB=")
			}
		}
	}
	return ""
}
