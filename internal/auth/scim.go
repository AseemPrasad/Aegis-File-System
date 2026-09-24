package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// SCIMUser represents a SCIM 2.0 User schema resource.
type SCIMUser struct {
	Schemas     []string `json:"schemas"`
	ID          string   `json:"id"`
	UserName    string   `json:"userName"`
	Name        SCIMName `json:"name"`
	Emails      []SCIMEmail `json:"emails"`
	Active      bool     `json:"active"`
	TenantID    string   `json:"tenantId,omitempty"`
	Meta        SCIMMeta `json:"meta"`
}

type SCIMName struct {
	Formatted  string `json:"formatted"`
	FamilyName string `json:"familyName"`
	GivenName  string `json:"givenName"`
}

type SCIMEmail struct {
	Value   string `json:"value"`
	Primary bool   `json:"primary"`
}

type SCIMMeta struct {
	ResourceType string `json:"resourceType"`
	Created      string `json:"created"`
	LastModified string `json:"lastModified"`
	Location     string `json:"location"`
}

// SCIMServer handles SCIM 2.0 user provisioning and deprovisioning API requests.
type SCIMServer struct {
	users map[string]SCIMUser // user_id -> user
	mu    sync.RWMutex
}

func NewSCIMServer() *SCIMServer {
	return &SCIMServer{
		users: make(map[string]SCIMUser),
	}
}

// HandleUsers routes SCIM 2.0 /scim/v2/Users endpoints.
func (s *SCIMServer) HandleUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/scim+json")

	switch r.Method {
	case http.MethodGet:
		s.listUsers(w, r)
	case http.MethodPost:
		s.createUser(w, r)
	default:
		http.Error(w, `{"status":"405","detail":"Method Not Allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (s *SCIMServer) createUser(w http.ResponseWriter, r *http.Request) {
	var user SCIMUser
	if err := json.NewDecoder(r.Body).Decode(&user); err != nil {
		http.Error(w, `{"status":"400","detail":"Invalid SCIM User Payload"}`, http.StatusBadRequest)
		return
	}

	user.ID = uuid.New().String()
	user.Schemas = []string{"urn:ietf:params:scim:schemas:core:2.0:User"}
	user.Active = true
	now := time.Now().Format(time.RFC3339)
	user.Meta = SCIMMeta{
		ResourceType: "User",
		Created:      now,
		LastModified: now,
		Location:     fmt.Sprintf("/scim/v2/Users/%s", user.ID),
	}

	s.mu.Lock()
	s.users[user.ID] = user
	s.mu.Unlock()

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(user)
}

func (s *SCIMServer) listUsers(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var list []SCIMUser
	for _, u := range s.users {
		list = append(list, u)
	}

	resp := map[string]interface{}{
		"schemas":      []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"},
		"totalResults": len(list),
		"startIndex":   1,
		"itemsPerPage": len(list),
		"Resources":    list,
	}

	_ = json.NewEncoder(w).Encode(resp)
}

// DeprovisionUser handles SCIM 2.0 user deprovisioning upon employee offboarding.
func (s *SCIMServer) DeprovisionUser(userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, exists := s.users[userID]
	if !exists {
		return fmt.Errorf("user %s not found", userID)
	}

	user.Active = false
	user.Meta.LastModified = time.Now().Format(time.RFC3339)
	s.users[userID] = user
	return nil
}
