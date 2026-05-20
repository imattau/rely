package nip11

type RelayInformationDocument struct {
	Name          string `json:"name,omitempty"`
	Description   string `json:"description,omitempty"`
	Software      string `json:"software,omitempty"`
	SupportedNIPs []any  `json:"supported_nips,omitempty"`
}
