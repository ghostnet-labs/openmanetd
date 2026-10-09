package radiocap

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// searchBudget bounds the backtracking assignment search, as in the offline
// model. Exhausting it yields an indeterminate result, never an admission.
const searchBudget = 50000

// NotEvaluated is the only value Decision.PhysicalQualification takes: the
// model never proves physical concurrency.
const NotEvaluated = "not_evaluated"

// Purpose says why a demand exists. It feeds explanations and the
// bounded-interruption policy.
type Purpose string

// Demand purposes.
const (
	PurposeMesh      Purpose = "mesh"           // the running mesh
	PurposeMigration Purpose = "mesh_migration" // target mesh during a migration
	PurposeEUD       Purpose = "eud_access"     // end-user-device access point
	PurposeOther     Purpose = "other"
)

// Channel is a channel identity. Width is part of it: a 20 MHz and a 40 MHz
// channel at the same center are two channels.
type Channel struct {
	Band      Band `json:"band"`
	CenterKHz int  `json:"center_khz"`
	WidthKHz  int  `json:"width_khz"`
}

// Demand is one interface that must run simultaneously with the others.
type Demand struct {
	ID      string  `json:"id"`
	Purpose Purpose `json:"purpose"`
	Role    Role    `json:"role"`
	Channel Channel `json:"channel"`
}

// Policy controls what Admit may do when capacity is short. The zero value
// is the default: qualified PHYs only, and reject rather than interrupt.
type Policy struct {
	// InterruptiblePurposes lists the purposes of current demands that a
	// bounded interruption may pause.
	InterruptiblePurposes []Purpose
	// MaxInterruption bounds an approved interruption.
	MaxInterruption time.Duration
	// AllowUnqualified lets unqualified (candidate) PHYs carry demands.
	AllowUnqualified bool
	// AllowBoundedInterruption approves pausing one current demand whose
	// purpose is in InterruptiblePurposes for at most MaxInterruption.
	AllowBoundedInterruption bool
}

// DefaultPolicy rejects on shortage and never interrupts.
func DefaultPolicy() Policy { return Policy{} }

// Outcome is the admission verdict.
type Outcome string

// Outcomes.
const (
	OutcomeAdmit  Outcome = "admit"
	OutcomeDefer  Outcome = "defer"
	OutcomeReject Outcome = "reject"
)

// Reason is the machine-readable cause of an Outcome.
type Reason string

// Reasons.
const (
	ReasonFits                Reason = "fits"
	ReasonBoundedInterruption Reason = "bounded_interruption_approved"
	ReasonPHYUnavailable      Reason = "phy_unavailable"
	ReasonCapacityShort       Reason = "capacity_short"
	ReasonNoEligiblePHY       Reason = "no_eligible_phy"
	ReasonUnqualifiedOnly     Reason = "unqualified_only"
	ReasonSearchBudget        Reason = "search_budget_exceeded"
)

// AlternativeKind names a change that would let the request fit.
type AlternativeKind string

// Alternative kinds.
const (
	// AltInterruptDemand: pausing the subject current demand frees capacity.
	AltInterruptDemand AlternativeKind = "interrupt_demand"
	// AltRestorePHY: the subject PHYs are qualified but unavailable/stale.
	AltRestorePHY AlternativeKind = "restore_phy"
	// AltQualifyPHY: the subject PHYs fit but are not qualified.
	AltQualifyPHY AlternativeKind = "qualify_phy"
)

// Alternative is one way the request could be satisfied.
type Alternative struct {
	Kind     AlternativeKind `json:"kind"`
	Subjects []string        `json:"subjects"`
	// Approved is true when the current policy already permits it.
	Approved bool `json:"approved"`
}

// Placement lists the demands assigned to one PHY.
type Placement struct {
	PHY     string   `json:"phy"`
	Demands []string `json:"demands"`
}

// Decision is the admission answer for a request.
type Decision struct {
	Outcome Outcome `json:"outcome"`
	Reason  Reason  `json:"reason"`
	// PhysicalQualification is always NotEvaluated.
	PhysicalQualification string `json:"physical_qualification"`
	// Explanation is a human-readable summary of the decision.
	Explanation     string        `json:"explanation"`
	Placements      []Placement   `json:"placements,omitempty"`
	Interrupt       []string      `json:"interrupt,omitempty"`
	Unplaceable     []string      `json:"unplaceable,omitempty"`
	Alternatives    []Alternative `json:"alternatives,omitempty"`
	MaxInterruption time.Duration `json:"max_interruption_ns,omitempty"`
	UsesUnqualified bool          `json:"uses_unqualified"`
}

// Status is the result of a single feasibility evaluation.
type Status string

// Statuses.
const (
	StatusFeasible      Status = "feasible"
	StatusInfeasible    Status = "infeasible"
	StatusIndeterminate Status = "indeterminate"
)

// Result is the outcome of Evaluate.
type Result struct {
	Status          Status      `json:"status"`
	Placements      []Placement `json:"placements,omitempty"`
	Unplaceable     []string    `json:"unplaceable,omitempty"`
	UsesUnqualified bool        `json:"uses_unqualified"`
}

// Model is an immutable inventory of PHYs.
type Model struct {
	phys []PHY
}

// NewModel validates and copies phys.
func NewModel(phys []PHY) (*Model, error) {
	if err := validatePHYs(phys); err != nil {
		return nil, err
	}

	cp := make([]PHY, len(phys))
	for i := range phys {
		cp[i] = clonePHY(&phys[i])
	}

	return &Model{phys: cp}, nil
}

func clonePHY(p *PHY) PHY {
	c := *p
	c.Bands = slices.Clone(p.Bands)
	c.SupportedTypes = slices.Clone(p.SupportedTypes)
	c.SoftwareTypes = slices.Clone(p.SoftwareTypes)
	c.Combinations = make([]Combination, len(p.Combinations))

	for i, combo := range p.Combinations {
		c.Combinations[i] = Combination{
			Limits:        make([]Limit, len(combo.Limits)),
			MaxInterfaces: combo.MaxInterfaces,
			MaxChannels:   combo.MaxChannels,
		}
		for j, l := range combo.Limits {
			c.Combinations[i].Limits[j] = Limit{Types: slices.Clone(l.Types), Max: l.Max}
		}
	}

	return c
}

// Capacities returns the capacity summary of every PHY in model order.
func (m *Model) Capacities() []Capacity {
	out := make([]Capacity, len(m.phys))
	for i := range m.phys {
		out[i] = m.phys[i].Capacity()
	}

	return out
}

// poolFilter selects which PHYs an allocation may use.
type poolFilter struct {
	unqualified bool
	unavailable bool
}

func (m *Model) pool(f poolFilter) []*PHY {
	out := make([]*PHY, 0, len(m.phys))
	for i := range m.phys {
		p := &m.phys[i]
		if (p.Available || f.unavailable) && (p.Qualified || f.unqualified) {
			out = append(out, p)
		}
	}

	return out
}

// Evaluate reports whether demands can run simultaneously on the available
// PHYs (qualified only unless allowUnqualified).
func (m *Model) Evaluate(demands []Demand, allowUnqualified bool) (Result, error) {
	if err := validateDemands(demands); err != nil {
		return Result{}, err
	}

	return allocate(m.pool(poolFilter{unqualified: allowUnqualified}), demands), nil
}

// Admit decides whether requested may start alongside current, the demands
// already running. On a shortage it never interrupts anything unless pol
// approves a bounded interruption; it defers when qualified PHYs that are
// unavailable would make room, and otherwise rejects with alternatives.
//
// Only one current demand is ever considered for interruption, and requested
// demands are never interrupted.
func (m *Model) Admit(current, requested []Demand, pol Policy) (Decision, error) {
	if len(requested) == 0 {
		return Decision{}, invalidf("no requested demands")
	}

	if pol.AllowBoundedInterruption && (pol.MaxInterruption <= 0 || len(pol.InterruptiblePurposes) == 0) {
		return Decision{}, invalidf("bounded interruption needs a positive bound and interruptible purposes")
	}

	all := make([]Demand, 0, len(current)+len(requested))
	all = append(all, current...)
	all = append(all, requested...)

	if err := validateDemands(all); err != nil {
		return Decision{}, err
	}

	base := poolFilter{unqualified: pol.AllowUnqualified}

	res := allocate(m.pool(base), all)
	switch res.Status {
	case StatusFeasible:
		return finish(Decision{
			Outcome: OutcomeAdmit, Reason: ReasonFits,
			Placements: res.Placements, UsesUnqualified: res.UsesUnqualified,
		}), nil
	case StatusIndeterminate:
		return finish(Decision{Outcome: OutcomeReject, Reason: ReasonSearchBudget}), nil
	case StatusInfeasible:
	}

	return m.diagnose(current, all, res, pol), nil
}

func (m *Model) diagnose(current, all []Demand, res Result, pol Policy) Decision {
	base := poolFilter{unqualified: pol.AllowUnqualified}

	var alts []Alternative

	restore := m.alternativeFor(all, poolFilter{unqualified: pol.AllowUnqualified, unavailable: true},
		func(p *PHY) bool { return !p.Available }, AltRestorePHY)
	if restore != nil {
		alts = append(alts, *restore)
	}

	var qualify *Alternative
	if !pol.AllowUnqualified {
		qualify = m.alternativeFor(all, poolFilter{unqualified: true},
			func(p *PHY) bool { return !p.Qualified }, AltQualifyPHY)
		if qualify != nil {
			alts = append(alts, *qualify)
		}
	}

	var (
		approvedID string // first approved interruption, by demand ID order
		approvedRs Result
	)

	pool := m.pool(base)
	ordered := slices.Clone(current)
	slices.SortFunc(ordered, func(a, b Demand) int { return strings.Compare(a.ID, b.ID) })

	rest := make([]Demand, 0, len(all))
	for _, d := range ordered {
		rest = rest[:0]

		for _, o := range all {
			if o.ID != d.ID {
				rest = append(rest, o)
			}
		}

		r := allocate(pool, rest)
		if r.Status != StatusFeasible {
			continue
		}

		ok := pol.AllowBoundedInterruption && slices.Contains(pol.InterruptiblePurposes, d.Purpose)
		alts = append(alts, Alternative{Kind: AltInterruptDemand, Subjects: []string{d.ID}, Approved: ok})

		if ok && approvedID == "" {
			approvedID = d.ID
			approvedRs = r
		}
	}

	dec := Decision{Alternatives: alts, Unplaceable: res.Unplaceable}

	switch {
	case restore != nil:
		dec.Outcome, dec.Reason = OutcomeDefer, ReasonPHYUnavailable
	case approvedID != "":
		dec.Outcome, dec.Reason = OutcomeAdmit, ReasonBoundedInterruption
		dec.Placements = approvedRs.Placements
		dec.UsesUnqualified = approvedRs.UsesUnqualified
		dec.Interrupt = []string{approvedID}
		dec.MaxInterruption = pol.MaxInterruption
		dec.Unplaceable = nil
	case len(res.Unplaceable) > 0 && qualify == nil:
		dec.Outcome, dec.Reason = OutcomeReject, ReasonNoEligiblePHY
	case qualify != nil:
		dec.Outcome, dec.Reason = OutcomeReject, ReasonUnqualifiedOnly
	default:
		dec.Outcome, dec.Reason = OutcomeReject, ReasonCapacityShort
	}

	return finish(dec)
}

// alternativeFor re-runs the allocation with a wider pool and, when it fits,
// names the PHYs from the wider pool (matched by extra) that it relied on.
func (m *Model) alternativeFor(all []Demand, f poolFilter, extra func(*PHY) bool, kind AlternativeKind) *Alternative {
	pool := m.pool(f)
	if !slices.ContainsFunc(pool, extra) {
		return nil
	}

	r := allocate(pool, all)
	if r.Status != StatusFeasible {
		return nil
	}

	var subjects []string

	for _, pl := range r.Placements {
		if extra(m.phy(pl.PHY)) {
			subjects = append(subjects, pl.PHY)
		}
	}

	if len(subjects) == 0 {
		return nil
	}

	return &Alternative{Kind: kind, Subjects: subjects}
}

func (m *Model) phy(name string) *PHY {
	for i := range m.phys {
		if m.phys[i].Name == name {
			return &m.phys[i]
		}
	}

	return nil
}

func finish(d Decision) Decision {
	d.PhysicalQualification = NotEvaluated
	d.Explanation = explain(&d)

	return d
}

func explain(d *Decision) string {
	var b strings.Builder

	switch d.Reason {
	case ReasonFits:
		b.WriteString("All demands fit on qualified radio capacity")

		if d.UsesUnqualified {
			b.WriteString(" (uses unqualified PHYs by policy)")
		}
	case ReasonBoundedInterruption:
		b.WriteString("Admitted by approved bounded interruption of ")
		b.WriteString(strings.Join(d.Interrupt, ", "))
		b.WriteString(" for at most ")
		b.WriteString(d.MaxInterruption.String())
	case ReasonPHYUnavailable:
		b.WriteString("Deferred: capacity exists only on PHYs that are currently unavailable")
	case ReasonCapacityShort:
		b.WriteString("Rejected: simultaneous demands exceed the PHYs' permitted interface combinations and channels")
	case ReasonNoEligiblePHY:
		b.WriteString("Rejected: no available qualified PHY supports ")
		b.WriteString(strings.Join(d.Unplaceable, ", "))
	case ReasonUnqualifiedOnly:
		b.WriteString("Rejected: demands fit only on unqualified PHYs")
	case ReasonSearchBudget:
		b.WriteString("Rejected: assignment search budget of ")
		b.WriteString(strconv.Itoa(searchBudget))
		b.WriteString(" states exhausted")
	}

	for _, a := range d.Alternatives {
		b.WriteString("; alternative ")
		b.WriteString(string(a.Kind))
		b.WriteString(": ")
		b.WriteString(strings.Join(a.Subjects, ", "))

		if a.Approved {
			b.WriteString(" (approved)")
		}
	}

	return b.String()
}

func validateDemands(ds []Demand) error {
	if len(ds) == 0 {
		return invalidf("at least one demand is required")
	}

	if len(ds) > MaxEntries {
		return invalidf("at most %d demands", MaxEntries)
	}

	seen := make(map[string]struct{}, len(ds))
	for i := range ds {
		d := &ds[i]
		if d.ID == "" {
			return invalidf("demand %d has no id", i)
		}

		if _, dup := seen[d.ID]; dup {
			return invalidf("duplicate demand %q", d.ID)
		}

		seen[d.ID] = struct{}{}

		if d.Role.IfType() == "" {
			return invalidf("demand %q has unknown role %q", d.ID, d.Role)
		}

		if d.Channel.Band == "" || d.Channel.CenterKHz <= 0 || d.Channel.WidthKHz <= 0 {
			return invalidf("demand %q needs a band and positive center and width", d.ID)
		}
	}

	return nil
}

// allocate assigns every demand to a PHY such that each PHY's set fits one
// of its combinations. It backtracks, most-constrained demand first, within
// searchBudget states.
func allocate(pool []*PHY, demands []Demand) Result {
	cands := make([][]int, len(demands))

	var unplaceable []string

	one := make([]*Demand, 1)
	for i := range demands {
		one[0] = &demands[i]
		for j, p := range pool {
			if fits(p, one) {
				cands[i] = append(cands[i], j)
			}
		}

		if len(cands[i]) == 0 {
			unplaceable = append(unplaceable, demands[i].ID)
		}
	}

	if len(unplaceable) > 0 {
		slices.Sort(unplaceable)

		return Result{Status: StatusInfeasible, Unplaceable: unplaceable}
	}

	order := make([]int, len(demands))
	for i := range order {
		order[i] = i
	}

	slices.SortFunc(order, func(a, b int) int {
		if n := len(cands[a]) - len(cands[b]); n != 0 {
			return n
		}

		return strings.Compare(demands[a].ID, demands[b].ID)
	})

	s := searcher{
		pool: pool, demands: demands, cands: cands, order: order,
		assigned: make([][]*Demand, len(pool)),
	}

	ok := s.search(0)
	if s.exhausted {
		return Result{Status: StatusIndeterminate}
	}

	if !ok {
		return Result{Status: StatusInfeasible}
	}

	return s.result()
}

type searcher struct {
	pool      []*PHY
	demands   []Demand
	cands     [][]int
	order     []int
	assigned  [][]*Demand
	steps     int
	exhausted bool
}

func (s *searcher) search(i int) bool {
	s.steps++
	if s.steps > searchBudget {
		s.exhausted = true

		return false
	}

	if i == len(s.order) {
		return true
	}

	d := &s.demands[s.order[i]]
	for _, j := range s.cands[s.order[i]] {
		s.assigned[j] = append(s.assigned[j], d)
		if fits(s.pool[j], s.assigned[j]) && s.search(i+1) {
			return true
		}

		s.assigned[j] = s.assigned[j][:len(s.assigned[j])-1]
		if s.exhausted {
			return false
		}
	}

	return false
}

func (s *searcher) result() Result {
	r := Result{Status: StatusFeasible, Placements: make([]Placement, 0, len(s.pool))}

	for j, group := range s.assigned {
		if len(group) == 0 {
			continue
		}

		ids := make([]string, len(group))
		for k, d := range group {
			ids[k] = d.ID
		}

		slices.Sort(ids)
		r.Placements = append(r.Placements, Placement{PHY: s.pool[j].Name, Demands: ids})

		if !s.pool[j].Qualified {
			r.UsesUnqualified = true
		}
	}

	slices.SortFunc(r.Placements, func(a, b Placement) int { return strings.Compare(a.PHY, b.PHY) })

	return r
}

// fits reports whether ds can run together on p under one combination. It
// does not allocate.
func fits(p *PHY, ds []*Demand) bool {
	channels := 0

	for i, d := range ds {
		if !slices.Contains(p.Bands, d.Channel.Band) {
			return false
		}

		if !slices.ContainsFunc(ds[:i], func(o *Demand) bool { return o.Channel == d.Channel }) {
			channels++
		}
	}

	if !p.SharingQualified && channels < len(ds) {
		return false
	}

	for ci := range p.Combinations {
		if comboFits(&p.Combinations[ci], ds, channels) {
			return true
		}
	}

	return false
}

func comboFits(c *Combination, ds []*Demand, channels int) bool {
	if len(ds) > c.MaxInterfaces || channels > c.MaxChannels {
		return false
	}

	for _, d := range ds {
		t := d.Role.IfType()

		if !slices.ContainsFunc(c.Limits, func(l Limit) bool { return slices.Contains(l.Types, t) }) {
			return false
		}
	}

	for _, l := range c.Limits {
		n := 0

		for _, d := range ds {
			if slices.Contains(l.Types, d.Role.IfType()) {
				n++
			}
		}

		if n > l.Max {
			return false
		}
	}

	return true
}
