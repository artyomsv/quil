package webgw

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/artyomsv/quil/internal/instances"
	"github.com/artyomsv/quil/internal/ipc"
)

// APIKeyHeader carries the port-scoped login key on /api requests. A custom
// header also forces a CORS preflight, which this server never grants.
const APIKeyHeader = "X-Quil-Key"

const (
	apiBodyMax    = 64 << 10
	instNameMax   = 128
	instValueMax  = 1024
	instFieldsMax = 32
)

// ClientExtras is what cmd/quil adds to /api/client from the machine's config.
// Keymap and Notifications stay `any` because their types live in packages
// webgw does not import (cmd/quil/web_client.go fills them); ClientInfo copies
// them into the JSON unchanged.
type ClientExtras struct {
	SandboxSignIn string
	SandboxImage  string
	Keymap        any
	Notifications any
}

// SandboxDefaults pre-fills the dialog's sandbox rows.
type SandboxDefaults struct {
	SignInDefault string `json:"sign_in_default"`
	ImageDefault  string `json:"image_default"`
}

// ClientInfo is GET /api/client.
type ClientInfo struct {
	Rights        string                       `json:"rights"`
	Plugins       []PluginDef                  `json:"plugins"`
	Categories    []CategoryDef                `json:"categories"`
	Instances     map[string][]instances.Saved `json:"instances"`
	Sandbox       SandboxDefaults              `json:"sandbox"`
	Keymap        any                          `json:"keymap"`
	Notifications any                          `json:"notifications"`
}

// apiAuth runs the checks every /api route shares (Host was checked by
// withSecurity). Origin is method-aware: a browser sends none on a
// same-origin GET, so a GET may omit it but never name another origin; a
// write needs this page's exact origin, as /login does. Fetch metadata, when
// present, must say same-origin (or none: typed into the address bar). Then
// the cookie, and the key compared in constant time (KeyValid), as web_open.
func (s *Server) apiAuth(w http.ResponseWriter, r *http.Request) (session string, ok bool) {
	w.Header().Set("Cache-Control", "no-store")
	origin := r.Header.Get("Origin")
	if r.Method == http.MethodGet {
		if origin != "" && !originAllowed(origin, r.Host) {
			w.WriteHeader(http.StatusForbidden)
			return "", false
		}
	} else if !originAllowed(origin, r.Host) {
		w.WriteHeader(http.StatusForbidden)
		return "", false
	}
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" && sfs != "none" {
		w.WriteHeader(http.StatusForbidden)
		return "", false
	}
	session = sessionOf(r)
	if !s.auth.KeyValid(session, r.Header.Get(APIKeyHeader)) {
		w.WriteHeader(http.StatusUnauthorized)
		return "", false
	}
	return session, true
}

var rightsRank = map[string]int{ipc.RightsReadOnly: 0, ipc.RightsStandard: 1, ipc.RightsFull: 2}

// sessionRights is the lowest rights level any live tab of session holds from
// its daemon login; "" when it holds none (no tab open, or the daemon closed
// them — a revoked token ends every tab). Never a value from the page.
func (s *Server) sessionRights(session string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	best, rank := "", len(rightsRank)
	for _, t := range s.tabs {
		if t.session != session {
			continue
		}
		if r, ok := rightsRank[t.rights]; ok && r < rank {
			best, rank = t.rights, r
		}
	}
	return best
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// handleClient is GET /api/client: what the page needs to draw the dialog
// and (Task 8) the keymap. Instances are read from disk per request, plugin
// definitions through the catalog.
func (s *Server) handleClient(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	session, ok := s.apiAuth(w, r)
	if !ok {
		return
	}
	var ex ClientExtras
	if s.cfg.ClientExtras != nil {
		ex = s.cfg.ClientExtras()
	}
	inst, err := s.loadInstances()
	if err != nil {
		inst = instances.Store{}
	}
	writeJSON(w, http.StatusOK, ClientInfo{
		Rights:        s.sessionRights(session),
		Plugins:       s.catalog.plugins(),
		Categories:    categories(),
		Instances:     inst,
		Sandbox:       SandboxDefaults{SignInDefault: ex.SandboxSignIn, ImageDefault: ex.SandboxImage},
		Keymap:        ex.Keymap,
		Notifications: ex.Notifications,
	})
}

type instanceBody struct {
	Plugin      string            `json:"plugin"`
	ID          string            `json:"id,omitempty"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Fields      map[string]string `json:"fields"`
}

var errBadInstance = errors.New("bad instance")

// validInstance accepts only a plugin that manages instances, field keys from
// its form with every required one filled, and bounded valid UTF-8 values.
func (s *Server) validInstance(b instanceBody) error {
	allowed := s.catalog.formFields(b.Plugin)
	if len(allowed) == 0 || b.Name == "" || len(b.Name) > instNameMax || !utf8.ValidString(b.Name) ||
		len(b.Description) > instValueMax || !utf8.ValidString(b.Description) || len(b.Fields) > instFieldsMax {
		return errBadInstance
	}
	for k, v := range b.Fields {
		if _, ok := allowed[k]; !ok || len(v) > instValueMax || !utf8.ValidString(v) {
			return errBadInstance
		}
	}
	for k, required := range allowed {
		if required && strings.TrimSpace(b.Fields[k]) == "" {
			return errBadInstance
		}
	}
	return nil
}

// newInstanceID draws 32 random bits until the id is not already used by any
// plugin in store: an id selects what a submit expands, so two instances
// sharing one would launch whichever is listed first.
func newInstanceID(r io.Reader, store instances.Store) (string, error) {
	used := map[string]bool{}
	for _, list := range store {
		for _, si := range list {
			used[si.ID] = true
		}
	}
	b := make([]byte, 4)
	for {
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}
		if id := hex.EncodeToString(b); !used[id] {
			return id, nil
		}
	}
}

// handleInstances is POST (create), PUT (edit) and DELETE (?plugin=&id=) on
// the instance file of the machine running quil web. Read-only sessions and
// sessions holding no daemon rights are refused; standard may save (launching
// one with raw args stays full-only at the daemon). One mutex serializes the
// read-modify-write; a TUI writing the file at the same moment is last writer
// wins. A file that does not parse is never replaced.
func (s *Server) handleInstances(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodDelete:
	default:
		w.Header().Set("Allow", "POST, PUT, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	session, ok := s.apiAuth(w, r)
	if !ok {
		return
	}
	if rights := s.sessionRights(session); rights == "" || rights == ipc.RightsReadOnly {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var b instanceBody
	if r.Method == http.MethodDelete {
		b.Plugin, b.ID = r.URL.Query().Get("plugin"), r.URL.Query().Get("id")
		if b.Plugin == "" || b.ID == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	} else {
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, apiBodyMax)).Decode(&b); err != nil || s.validInstance(b) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Method == http.MethodPut && b.ID == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}

	s.instMu.Lock()
	defer s.instMu.Unlock()
	store, err := instances.Load(s.cfg.InstancesPath)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "instances.json does not parse; fix it in the TUI first"})
		return
	}
	list := store[b.Plugin]
	save := func() bool {
		if err := instances.Save(s.cfg.InstancesPath, store); err != nil {
			s.cfg.Logf("instances.json not written: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return false
		}
		return true
	}
	switch r.Method {
	case http.MethodPost:
		id, err := newInstanceID(s.cfg.Rand, store)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		saved := instances.Saved{ID: id, Name: b.Name, Fields: b.Fields, Description: b.Description}
		store[b.Plugin] = append(list, saved)
		if save() {
			writeJSON(w, http.StatusCreated, saved)
		}
		return
	case http.MethodPut:
		for i := range list {
			if list[i].ID == b.ID {
				list[i] = instances.Saved{ID: b.ID, Name: b.Name, Fields: b.Fields, Description: b.Description}
				if save() {
					writeJSON(w, http.StatusOK, list[i])
				}
				return
			}
		}
	case http.MethodDelete:
		for i := range list {
			if list[i].ID == b.ID {
				store[b.Plugin] = append(list[:i:i], list[i+1:]...)
				if save() {
					w.WriteHeader(http.StatusNoContent)
				}
				return
			}
		}
	}
	w.WriteHeader(http.StatusNotFound)
}
