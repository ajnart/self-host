package helpers

type ExternalCredential struct {
	Kind  string `json:"kind"`
	Value string `json:"-"`
}

type ExternalConnection struct {
	ID          string               `json:"id"`
	Kind        string               `json:"kind"`
	Endpoint    string               `json:"-"`
	Credentials []ExternalCredential `json:"-"`
	Selection   map[string]string    `json:"-"`
	Removing    bool                 `json:"removing,omitempty"`
}
