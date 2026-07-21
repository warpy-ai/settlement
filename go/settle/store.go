package settle

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DirName is the settlement state directory at the repo root.
const DirName = ".settlement"

// Store reads and writes .settlement/ state.
type Store struct {
	Root string // absolute path of the .settlement directory
}

// FindStore walks up from startDir looking for a .settlement directory.
func FindStore(startDir string) (*Store, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return nil, err
	}
	for {
		candidate := filepath.Join(dir, DirName)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return &Store{Root: candidate}, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, fmt.Errorf("no %s directory found from %s upward (run `settle init` at the repo root)", DirName, startDir)
		}
		dir = parent
	}
}

// Init scaffolds .settlement/ in dir. It refuses to overwrite existing state.
func Init(dir string) (*Store, error) {
	root := filepath.Join(dir, DirName)
	if _, err := os.Stat(root); err == nil {
		return nil, fmt.Errorf("%s already exists", root)
	}
	if err := os.MkdirAll(filepath.Join(root, "verdicts"), 0o755); err != nil {
		return nil, err
	}
	s := &Store{Root: root}

	if err := writeJSON(s.configPath(), DefaultConfig()); err != nil {
		return nil, err
	}

	ledger := Ledger{Schema: SchemaLedger, Personas: map[string]*LedgerEntry{}}
	for _, p := range DefaultConfig().Panel.Personas {
		ledger.Personas[p] = &LedgerEntry{VotingPower: 1.0, UpdatedAt: time.Now().UTC()}
	}
	if err := writeJSON(s.ledgerPath(), ledger); err != nil {
		return nil, err
	}

	if err := os.WriteFile(s.decisionsPath(), nil, 0o644); err != nil {
		return nil, err
	}

	if err := writeJSON(s.skillsPath(), SkillLedger{Schema: SchemaSkills, Skills: map[string]*SkillEntry{}}); err != nil {
		return nil, err
	}

	if err := writeJSON(s.loopsPath(), LoopLedger{Schema: SchemaLoops, Loops: map[string]*LoopEntry{}}); err != nil {
		return nil, err
	}
	// verdicts/ holds per-review scratch; keep it out of git.
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("verdicts/\n"), 0o644); err != nil {
		return nil, err
	}
	// Decisions recorded on parallel branches (worktree-based multi-task
	// hosts) are append-only; union merge combines them without conflicts.
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("decisions.jsonl merge=union\n"), 0o644); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) configPath() string    { return filepath.Join(s.Root, "config.json") }
func (s *Store) ledgerPath() string    { return filepath.Join(s.Root, "ledger.json") }
func (s *Store) decisionsPath() string { return filepath.Join(s.Root, "decisions.jsonl") }
func (s *Store) skillsPath() string    { return filepath.Join(s.Root, "skills.json") }
func (s *Store) loopsPath() string     { return filepath.Join(s.Root, "loops.json") }

// VerdictsDir returns the scratch directory for a task's verdict files.
func (s *Store) VerdictsDir(taskID string) string {
	return filepath.Join(s.Root, "verdicts", taskID)
}

func (s *Store) LoadConfig() (Config, error) {
	var cfg Config
	err := readJSON(s.configPath(), &cfg)
	return cfg, err
}

func (s *Store) LoadLedger() (Ledger, error) {
	var l Ledger
	err := readJSON(s.ledgerPath(), &l)
	if err == nil && l.Personas == nil {
		l.Personas = map[string]*LedgerEntry{}
	}
	return l, err
}

func (s *Store) SaveLedger(l Ledger) error {
	return writeJSON(s.ledgerPath(), l)
}

// LoadSkillLedger reads the adequacy ledger, tolerating stores created before
// skills.json existed (returns an empty ledger).
func (s *Store) LoadSkillLedger() (SkillLedger, error) {
	var sl SkillLedger
	err := readJSON(s.skillsPath(), &sl)
	if os.IsNotExist(err) {
		return SkillLedger{Schema: SchemaSkills, Skills: map[string]*SkillEntry{}}, nil
	}
	if err == nil && sl.Skills == nil {
		sl.Skills = map[string]*SkillEntry{}
	}
	return sl, err
}

func (s *Store) SaveSkillLedger(sl SkillLedger) error {
	return writeJSON(s.skillsPath(), sl)
}

// LoadLoopLedger reads the loop trust ledger, tolerating stores created before
// loops.json existed (returns an empty ledger).
func (s *Store) LoadLoopLedger() (LoopLedger, error) {
	var ll LoopLedger
	err := readJSON(s.loopsPath(), &ll)
	if os.IsNotExist(err) {
		return LoopLedger{Schema: SchemaLoops, Loops: map[string]*LoopEntry{}}, nil
	}
	if err == nil && ll.Loops == nil {
		ll.Loops = map[string]*LoopEntry{}
	}
	return ll, err
}

func (s *Store) SaveLoopLedger(ll LoopLedger) error {
	return writeJSON(s.loopsPath(), ll)
}

// QuarantinedSkills returns the set of note ids currently below adequacy, to be
// excluded from auto-injected guidance. It never errors: a missing or unreadable
// ledger means nothing is quarantined.
func (s *Store) QuarantinedSkills() map[string]bool {
	sl, err := s.LoadSkillLedger()
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for id, e := range sl.Skills {
		if e.Quarantined {
			out[id] = true
		}
	}
	return out
}

// AppendDecision appends a decision line to decisions.jsonl.
func (s *Store) AppendDecision(d Decision) error {
	return s.appendLine(d)
}

// AppendOutcomeEvent appends a ground-truth outcome event to decisions.jsonl.
func (s *Store) AppendOutcomeEvent(e OutcomeEvent) error {
	return s.appendLine(e)
}

func (s *Store) appendLine(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.decisionsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

// ReadDecisions returns all decisions with any later outcome events folded
// into their Result field, oldest first.
func (s *Store) ReadDecisions() ([]Decision, error) {
	f, err := os.Open(s.decisionsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var decisions []Decision
	index := map[string]int{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var probe struct {
			Schema string `json:"schema"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			return nil, fmt.Errorf("corrupt decisions.jsonl line: %w", err)
		}
		switch probe.Schema {
		case SchemaDecision:
			var d Decision
			if err := json.Unmarshal(line, &d); err != nil {
				return nil, err
			}
			index[d.ID] = len(decisions)
			decisions = append(decisions, d)
		case SchemaOutcome:
			var e OutcomeEvent
			if err := json.Unmarshal(line, &e); err != nil {
				return nil, err
			}
			if i, ok := index[e.DecisionID]; ok {
				decisions[i].Result = e.Result
			}
		}
	}
	return decisions, scanner.Err()
}

// GetDecision returns the decision with the given id.
func (s *Store) GetDecision(id string) (Decision, error) {
	decisions, err := s.ReadDecisions()
	if err != nil {
		return Decision{}, err
	}
	for _, d := range decisions {
		if d.ID == id {
			return d, nil
		}
	}
	return Decision{}, fmt.Errorf("decision %s not found", id)
}

func readJSON(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func writeJSON(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
