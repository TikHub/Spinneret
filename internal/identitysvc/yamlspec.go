package identitysvc

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"

	"github.com/TikHub/Spinneret/internal/identity"
)

// MaxSpecYAMLBytes is the maximum size of an identity type YAML spec.
const MaxSpecYAMLBytes = 1 << 20

// parseTypeYAMLForSite parses an identity type spec whose site is forced to
// siteName. The returned YAML is the source with the top-level "site" key set
// to siteName (inserted after "name" when missing); the original bytes are
// returned unchanged when they already name the site. Comments survive the
// rewrite, indentation is normalized to two spaces.
func parseTypeYAMLForSite(src []byte, siteName string) (*identity.TypeSpec, []byte, error) {
	if len(src) > MaxSpecYAMLBytes {
		return nil, nil, invalid("spec_yaml must be at most %d bytes", MaxSpecYAMLBytes)
	}
	out, err := setYAMLSite(src, siteName)
	if err != nil {
		return nil, nil, err
	}
	spec, err := identity.ParseTypeYAML(out)
	if err != nil {
		return nil, nil, err
	}
	return spec, out, nil
}

// yamlSiteName returns the scalar top-level "site" value of a YAML spec, or
// "" when it is absent or the document cannot be read.
func yamlSiteName(src []byte) string {
	root, err := decodeSingleDocument(src)
	if err != nil {
		return ""
	}
	mapping := root.Content[0]
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if k, v := mapping.Content[i], mapping.Content[i+1]; k.Value == "site" && v.Kind == yaml.ScalarNode {
			return v.Value
		}
	}
	return ""
}

// setYAMLSite returns src with its top-level site set to siteName.
func setYAMLSite(src []byte, siteName string) ([]byte, error) {
	root, err := decodeSingleDocument(src)
	if err != nil {
		return nil, err
	}
	mapping := root.Content[0]
	insertAt := 0
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key, value := mapping.Content[i], mapping.Content[i+1]
		switch key.Value {
		case "site":
			if value.Kind == yaml.ScalarNode && value.Value == siteName && value.Tag != "!!null" {
				return src, nil
			}
			mapping.Content[i+1] = siteScalar(siteName)
			return encodeYAML(root)
		case "name":
			insertAt = i + 2
		}
	}
	content := make([]*yaml.Node, 0, len(mapping.Content)+2)
	content = append(content, mapping.Content[:insertAt]...)
	content = append(content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "site"}, siteScalar(siteName))
	content = append(content, mapping.Content[insertAt:]...)
	mapping.Content = content
	return encodeYAML(root)
}

func siteScalar(name string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
}

// decodeSingleDocument decodes src into a document node whose root is a mapping.
func decodeSingleDocument(src []byte) (*yaml.Node, error) {
	if len(bytes.TrimSpace(src)) == 0 {
		return nil, invalid("identity type spec is empty")
	}
	dec := yaml.NewDecoder(bytes.NewReader(src))
	var root yaml.Node
	if err := dec.Decode(&root); err != nil {
		return nil, invalid("parse identity type yaml: %v", err).WithCause(err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, invalid("parse identity type yaml: expected a single YAML document")
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, invalid("parse identity type yaml: the document must be a mapping")
	}
	return &root, nil
}

func encodeYAML(root *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, fmt.Errorf("encode identity type yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode identity type yaml: %w", err)
	}
	return buf.Bytes(), nil
}
