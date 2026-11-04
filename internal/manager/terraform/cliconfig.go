package terraform

import (
	"encoding/json/v2"
	"fmt"
)

// CLIConfig is what deps reads of a Terraform CLI configuration file (`.terraformrc`,
// `*.tfrc`, `*.tfrc.json`): registry credentials and the credentials helper.
type CLIConfig struct {
	Credentials map[string]string // host -> token of the `credentials "<host>"` blocks
	Helper      string            // label of the `credentials_helper` block; "" when there is none
	HelperArgs  []string          // its `args`
}

// ParseCLIConfig parses a CLI configuration file in HCL syntax.
func ParseCLIConfig(src []byte) (CLIConfig, error) {
	b, err := parseHCL(src)
	if err != nil {
		return CLIConfig{}, err
	}
	var c CLIConfig
	for _, blk := range b.blocks {
		if len(blk.labels) != 1 {
			continue
		}
		switch blk.typ {
		case "credentials":
			for _, a := range blk.body.attrs {
				if a.name == "token" && a.val.kind == kindString {
					if c.Credentials == nil {
						c.Credentials = map[string]string{}
					}
					c.Credentials[blk.labels[0]] = a.val.str
				}
			}
		case "credentials_helper":
			c.Helper, c.HelperArgs = blk.labels[0], nil
			for _, a := range blk.body.attrs {
				if a.name != "args" {
					continue
				}
				if c.HelperArgs, err = stringList(src, a); err != nil {
					return CLIConfig{}, err
				}
			}
		}
	}
	return c, nil
}

// stringList re-reads the value of a as a list of string literals.
func stringList(src []byte, a attr) ([]string, error) {
	p := hclParser{src: src, pos: a.off + len(a.name)}
	if err := p.space(false); err != nil {
		return nil, err
	}
	p.pos++ // '='
	if err := p.space(false); err != nil {
		return nil, err
	}
	if p.peek() != '[' {
		return nil, p.errorf("%s: want a list of strings", a.name)
	}
	p.pos++
	out := []string{}
	for {
		if err := p.space(true); err != nil {
			return nil, err
		}
		switch p.peek() {
		case ']':
			return out, nil
		case ',':
			p.pos++
			continue
		case '"':
			s, _, literal, err := p.quoted()
			if err != nil {
				return nil, err
			}
			if !literal {
				return nil, p.errorf("%s: templates are not supported", a.name)
			}
			out = append(out, s)
		default:
			return nil, p.errorf("%s: want a list of strings", a.name)
		}
	}
}

// ParseCLIConfigJSON parses a CLI configuration file in JSON syntax (`credentials.tfrc.json`).
func ParseCLIConfigJSON(src []byte) (CLIConfig, error) {
	var doc struct {
		Credentials map[string]struct {
			Token string `json:"token"`
		} `json:"credentials"`
		CredentialsHelper map[string]struct {
			Args []string `json:"args"`
		} `json:"credentials_helper"`
	}
	if err := json.Unmarshal(src, &doc); err != nil {
		return CLIConfig{}, fmt.Errorf("malformed CLI config: %w", err)
	}
	var c CLIConfig
	for host, cred := range doc.Credentials {
		if cred.Token == "" {
			continue
		}
		if c.Credentials == nil {
			c.Credentials = map[string]string{}
		}
		c.Credentials[host] = cred.Token
	}
	for name, h := range doc.CredentialsHelper {
		c.Helper, c.HelperArgs = name, h.Args
	}
	return c, nil
}
