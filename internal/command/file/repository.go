package file

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"homeagent/internal/command"
)

type data struct {
	SchemaVersion int               `json:"schema_version"`
	Commands      []command.Command `json:"commands"`
}
type Repository struct {
	mu       sync.Mutex
	path     string
	commands map[command.ID]command.Command
}

func Open(path string) (*Repository, error) {
	r := &Repository{path: path, commands: map[command.ID]command.Command{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if info, statErr := os.Stat(path); statErr != nil {
		return nil, statErr
	} else if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("unsafe command store permissions %o", info.Mode().Perm())
	}
	var d data
	if err = json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("decode commands: %w", err)
	}
	if d.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported command schema %d", d.SchemaVersion)
	}
	for _, c := range d.Commands {
		r.commands[c.ID] = c
	}
	return r, nil
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func idem(c command.Command) string {
	return c.RequestedBy.Type + "\x00" + c.RequestedBy.ID + "\x00" + c.DeviceID + "\x00" + string(c.Kind) + "\x00" + c.IdempotencyKey
}

func (r *Repository) CreateOrGet(req command.CreateRequest) (command.Command, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	requestDigest := digest(req.Request)
	for _, c := range r.commands {
		if req.IdempotencyKey != "" && idem(c) == req.RequestedBy.Type+"\x00"+req.RequestedBy.ID+"\x00"+req.DeviceID+"\x00"+string(req.Kind)+"\x00"+req.IdempotencyKey {
			if c.RequestDigest != requestDigest {
				return command.Command{}, false, command.ErrIdempotencyConflict
			}
			return c, false, nil
		}
		if c.DeviceID == req.DeviceID && c.Kind == req.Kind && !c.Terminal() {
			return command.Command{}, false, command.ErrCommandInProgress
		}
		if !c.Terminal() && c.DeviceID == req.DeviceID && ((c.Kind == command.KindShutdown && req.Kind == command.KindUpgrade) || (c.Kind == command.KindUpgrade && req.Kind == command.KindShutdown)) {
			return command.Command{}, false, command.ErrCommandInProgress
		}
	}
	id, err := newID()
	if err != nil {
		return command.Command{}, false, err
	}
	now := time.Now().UTC()
	protocol := req.Protocol
	if protocol == 0 {
		protocol = 1
	}
	c := command.Command{ID: id, Kind: req.Kind, DeviceID: req.DeviceID, Status: command.StatusQueued, RequestedBy: req.RequestedBy, IdempotencyKey: req.IdempotencyKey, Request: clone(req.Request), RequestDigest: requestDigest, CreatedAt: now, UpdatedAt: now, TimeoutPolicy: req.TimeoutPolicy, Revision: 1, Protocol: protocol, Projection: command.ProjectionState{Status: "pending"}}
	r.commands[id] = c
	if err = r.write(); err != nil {
		delete(r.commands, id)
		return command.Command{}, false, err
	}
	return c, true, nil
}
func clone(b []byte) []byte { return append([]byte(nil), b...) }
func (r *Repository) Get(id command.ID) (command.Command, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.commands[id]
	if !ok {
		return command.Command{}, command.ErrNotFound
	}
	return c, nil
}
func valid(from, to command.Status) bool {
	if from == to {
		return true
	}
	switch from {
	case command.StatusQueued:
		return to == command.StatusDispatching || to == command.StatusCanceled || to == command.StatusInterrupted
	case command.StatusDispatching:
		return to == command.StatusDispatched || to == command.StatusAccepted || to == command.StatusSucceeded || to == command.StatusFailed || to == command.StatusCanceled || to == command.StatusInterrupted
	case command.StatusDispatched:
		return to == command.StatusAccepted || to == command.StatusSucceeded || to == command.StatusFailed || to == command.StatusTimedOut || to == command.StatusCanceled || to == command.StatusInterrupted || to == command.StatusLegacyUntracked
	case command.StatusAccepted:
		return to == command.StatusSucceeded || to == command.StatusFailed || to == command.StatusTimedOut || to == command.StatusInterrupted
	}
	return false
}
func (r *Repository) Transition(id command.ID, rev uint64, t command.Transition) (command.Command, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.commands[id]
	if !ok {
		return command.Command{}, command.ErrNotFound
	}
	if c.Revision != rev {
		return command.Command{}, command.ErrConflict
	}
	if !valid(c.Status, t.Status) {
		return command.Command{}, command.ErrInvalidTransition
	}
	if c.Status == t.Status {
		return c, nil
	}
	now := t.At.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	old := c
	c.Status = t.Status
	c.UpdatedAt = now
	c.Revision++
	c.ErrorCode = t.ErrorCode
	c.ErrorMessage = t.ErrorMessage
	c.AttemptID = t.AttemptID
	if t.Status == command.StatusDispatching {
		d := now.Add(c.TimeoutPolicy.Accept)
		c.AcceptDeadline = &d
	}
	if t.Status == command.StatusDispatched || t.Status == command.StatusAccepted || t.Status == command.StatusSucceeded || t.Status == command.StatusFailed {
		if c.DispatchedAt == nil {
			v := now
			c.DispatchedAt = &v
		}
	}
	if t.Status == command.StatusAccepted {
		v := now
		c.AcceptedAt = &v
		d := now.Add(c.TimeoutPolicy.Finish)
		c.FinishDeadline = &d
	}
	if t.Status == command.StatusSucceeded || t.Status == command.StatusFailed {
		c.Result = clone(t.Result)
		c.FinalAckDigest = t.FinalAckDigest
		c.OutcomeRevision = c.Revision
		c.ProjectionInputDigest = t.ProjectionInputDigest
	}
	if c.Terminal() {
		v := now
		c.FinishedAt = &v
	}
	r.commands[id] = c
	if err := r.write(); err != nil {
		r.commands[id] = old
		return command.Command{}, err
	}
	return c, nil
}
func (r *Repository) List(f command.Filter) (command.Page, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var a []command.Command
	for _, c := range r.commands {
		if f.DeviceID != "" && c.DeviceID != f.DeviceID || f.Kind != "" && c.Kind != f.Kind || f.Status != "" && c.Status != f.Status {
			continue
		}
		a = append(a, c)
	}
	sort.Slice(a, func(i, j int) bool {
		if a[i].CreatedAt.Equal(a[j].CreatedAt) {
			return a[i].ID > a[j].ID
		}
		return a[i].CreatedAt.After(a[j].CreatedAt)
	})
	if f.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(f.Cursor)
		if err != nil {
			return command.Page{}, fmt.Errorf("invalid cursor")
		}
		parts := strings.SplitN(string(raw), "|", 2)
		if len(parts) != 2 {
			return command.Page{}, fmt.Errorf("invalid cursor")
		}
		cursorTime, err := time.Parse(time.RFC3339Nano, parts[0])
		if err != nil {
			return command.Page{}, fmt.Errorf("invalid cursor")
		}
		filtered := a[:0]
		for _, c := range a {
			if c.CreatedAt.Before(cursorTime) || (c.CreatedAt.Equal(cursorTime) && string(c.ID) < parts[1]) {
				filtered = append(filtered, c)
			}
		}
		a = filtered
	}
	n := f.Limit
	if n <= 0 || n > 100 {
		n = 50
	}
	p := command.Page{}
	if len(a) > n {
		last := a[n-1]
		p.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(last.CreatedAt.Format(time.RFC3339Nano) + "|" + string(last.ID)))
		a = a[:n]
	}
	p.Commands = a
	return p, nil
}
func (r *Repository) ListExpired(now time.Time, limit int) ([]command.Command, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []command.Command
	for _, c := range r.commands {
		if c.Terminal() {
			continue
		}
		d := c.AcceptDeadline
		if c.Status == command.StatusAccepted {
			d = c.FinishDeadline
		}
		if d != nil && !d.After(now) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		di := out[i].AcceptDeadline
		if out[i].Status == command.StatusAccepted {
			di = out[i].FinishDeadline
		}
		dj := out[j].AcceptDeadline
		if out[j].Status == command.StatusAccepted {
			dj = out[j].FinishDeadline
		}
		return di.Before(*dj)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (r *Repository) InterruptNonTerminal(now time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.commands
	n := 0
	copyMap := map[command.ID]command.Command{}
	for id, c := range r.commands {
		if !c.Terminal() {
			c.Status = command.StatusInterrupted
			c.Revision++
			c.UpdatedAt = now.UTC()
			v := now.UTC()
			c.FinishedAt = &v
			n++
		}
		copyMap[id] = c
	}
	r.commands = copyMap
	if n > 0 {
		if err := r.write(); err != nil {
			r.commands = old
			return 0, err
		}
	}
	return n, nil
}
func (r *Repository) AppendLateAck(id command.ID, rev uint64, ack command.LateAck) (command.Command, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.commands[id]
	if !ok {
		return command.Command{}, command.ErrNotFound
	}
	if c.Revision != rev {
		return command.Command{}, command.ErrConflict
	}
	for _, existing := range c.LateAcks {
		if existing.Digest == ack.Digest {
			return c, nil
		}
	}
	if len(c.LateAcks) > 0 {
		return command.Command{}, command.ErrInvalidTransition
	}
	old := c
	c.LateAcks = append(c.LateAcks, ack)
	c.Revision++
	c.UpdatedAt = ack.ReceivedAt
	r.commands[id] = c
	if err := r.write(); err != nil {
		r.commands[id] = old
		return command.Command{}, err
	}
	return c, nil
}

func (r *Repository) UpdateProjection(id command.ID, rev uint64, p command.ProjectionState) (command.Command, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.commands[id]
	if !ok {
		return command.Command{}, command.ErrNotFound
	}
	if c.Revision != rev {
		return command.Command{}, command.ErrConflict
	}
	old := c
	p.Revision = c.Projection.Revision + 1
	c.Projection = p
	c.Revision++
	c.UpdatedAt = time.Now().UTC()
	r.commands[id] = c
	if err := r.write(); err != nil {
		r.commands[id] = old
		return command.Command{}, err
	}
	return c, nil
}
func (r *Repository) ListProjectionPending(limit int) ([]command.Command, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []command.Command
	for _, c := range r.commands {
		if (c.Status == command.StatusSucceeded || c.Status == command.StatusFailed) && c.Projection.Status == "pending" {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *Repository) UpdateProgress(id command.ID, rev uint64, p command.UpgradeProgress) (command.Command, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.commands[id]
	if !ok {
		return command.Command{}, command.ErrNotFound
	}
	if c.Revision != rev {
		return command.Command{}, command.ErrConflict
	}
	old := c
	c.Progress = &p
	c.Revision++
	c.UpdatedAt = time.Now().UTC()
	r.commands[id] = c
	if err := r.write(); err != nil {
		r.commands[id] = old
		return command.Command{}, err
	}
	return c, nil
}

func (r *Repository) write() error {
	drop := r.pruneIDsLocked(time.Now().UTC())
	if err := os.MkdirAll(filepath.Dir(r.path), 0700); err != nil {
		return err
	}
	d := data{SchemaVersion: 1}
	for id, c := range r.commands {
		if drop[id] {
			continue
		}
		d.Commands = append(d.Commands, c)
	}
	sort.Slice(d.Commands, func(i, j int) bool { return d.Commands[i].ID < d.Commands[j].ID })
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.path), ".commands-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(b)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, r.path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if dir, openErr := os.Open(filepath.Dir(r.path)); openErr == nil {
			_ = dir.Sync()
			_ = dir.Close()
		}
	}
	for id := range drop {
		delete(r.commands, id)
	}
	return nil
}

func (r *Repository) pruneIDsLocked(now time.Time) map[command.ID]bool {
	drop := map[command.ID]bool{}
	if len(r.commands) <= 1000 {
		return drop
	}
	var candidates []command.Command
	for _, c := range r.commands {
		if c.Terminal() && c.FinishedAt != nil && c.FinishedAt.Before(now.Add(-30*24*time.Hour)) {
			candidates = append(candidates, c)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].FinishedAt.Before(*candidates[j].FinishedAt) })
	remaining := len(r.commands)
	for _, c := range candidates {
		if remaining <= 1000 {
			break
		}
		drop[c.ID] = true
		remaining--
	}
	return drop
}
func newID() (command.ID, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return command.ID("cmd_" + hex.EncodeToString(b)), nil
}
