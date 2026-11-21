package gitlab

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net/mail"
	"sync"

	"github.com/ordo-systems/wrench/internal/config"
)

// AccountID derives a stable, positive GitLab user ID from an email, so the mock needs no storage and keeps IDs
// across restarts.
func AccountID(email string) int64 {
	sum := sha256.Sum256([]byte(config.NormalizeEmail(email)))
	return int64(binary.BigEndian.Uint64(sum[:8])&(1<<62-1)) + 1
}

// ConfigDirectory serves the emails of the Wrench configuration plus any email signed in through the picker.
// Every ID is an active account, including IDs the mock has not seen since it started: a restarted mock must
// never make Wrench block a user.
func ConfigDirectory(emails []string) Directory {
	d := &configDirectory{byID: map[int64]string{}}
	for _, e := range emails {
		d.candidates = append(d.candidates, e)
		d.byID[AccountID(e)] = config.NormalizeEmail(e)
	}
	return d
}

type configDirectory struct {
	candidates []string

	mu   sync.Mutex
	byID map[int64]string
}

func (d *configDirectory) Lookup(id int64) (Account, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return Account{ID: id, Email: d.byID[id], EmailVerified: true, State: "active"}, true
}

func (d *configDirectory) Candidates() []string { return d.candidates }

func (d *configDirectory) SignIn(email string) (int64, error) {
	email = config.NormalizeEmail(email)
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email || addr.Name != "" {
		return 0, errors.New("enter an email address")
	}
	id := AccountID(email)
	d.mu.Lock()
	d.byID[id] = email
	d.mu.Unlock()
	return id, nil
}
