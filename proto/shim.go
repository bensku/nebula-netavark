package proto

// nebula-netavark-plugin's invocation for nebula-netavarkd to read
type PluginMessage struct {
	Args []string `json:"args"`
	Raw  []byte   `json:"raw"`
}
