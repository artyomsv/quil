package ipc

// Token management. Class `local`: the daemon accepts these from
// the local socket only, whatever a TCP conn's rights.
const (
	MsgTokenCreateReq  = "token_create_req"
	MsgTokenCreateResp = "token_create_resp"
	MsgTokenListReq    = "token_list_req"
	MsgTokenListResp   = "token_list_resp"
	MsgTokenRevokeReq  = "token_revoke_req"
	MsgTokenRevokeResp = "token_revoke_resp"
)

// TokenCreateReqPayload asks the daemon to mint a token.
type TokenCreateReqPayload struct {
	Name    string `json:"name"`
	Rights  string `json:"rights,omitempty"`  // "" = standard
	Expires string `json:"expires,omitempty"` // "<N>d" or "never"; "" = 90d
}

// TokenCreateRespPayload carries the token ONCE. Nothing logs it.
type TokenCreateRespPayload struct {
	Token   string `json:"token,omitempty"`
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Rights  string `json:"rights,omitempty"`
	Expires string `json:"expires,omitempty"` // RFC 3339; "" = never
	Error   string `json:"error,omitempty"`
}

// TokenInfo is one row of `quil clients token list`. It never carries the
// token or its keys.
type TokenInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Rights   string `json:"rights"`
	Created  string `json:"created"`
	Expires  string `json:"expires,omitempty"`
	LastUsed string `json:"last_used,omitempty"`
}

type TokenListRespPayload struct {
	Tokens []TokenInfo `json:"tokens"`
	Error  string      `json:"error,omitempty"`
}

// TokenRevokeReqPayload names a token by id or (case-insensitive) name.
type TokenRevokeReqPayload struct {
	Target string `json:"target"`
}

type TokenRevokeRespPayload struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Closed int    `json:"closed"`
	Error  string `json:"error,omitempty"`
}
