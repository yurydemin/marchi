package i18n

import "github.com/BurntSushi/toml"

// tomlUnmarshal adapts BurntSushi/toml's Unmarshal to go-i18n's
// UnmarshalFunc signature (registered against the ".toml" extension in
// mustLoadBundle).
func tomlUnmarshal(data []byte, v interface{}) error {
	return toml.Unmarshal(data, v)
}
