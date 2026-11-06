package launch

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/trigger"
)

// maxInitials is how many crew initials a list row carries for its avatar stack.
const maxInitials = 5

// add counts one crew row the way Detail does: In is crew who queued,
// posted or shared another way, Total is every crew row but the suggested ones.
// The captain's own row is not crew.
func (c *Counts) add(state string) {
	switch state {
	case trigger.StateSuggested:
	case "queued", "posted", "copied":
		c.In++
		c.Total++
	default:
		c.Total++
	}
}

// initialsOf is the first letters of the first two words of a name: "Maya
// Kapoor" is MK, "carol" is C.
func initialsOf(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var out []rune
	for _, w := range words {
		out = append(out, unicode.ToUpper([]rune(w)[0]))
		if len(out) == 2 {
			break
		}
	}
	return string(out)
}

type crewPerson struct {
	name string
	in   bool
}

// orderPeople lists the people who are in first, then by name, so the avatar
// stack does not change between reloads.
func orderPeople(ps []crewPerson) {
	slices.SortStableFunc(ps, func(a, b crewPerson) int {
		if a.in != b.in {
			if a.in {
				return -1
			}
			return 1
		}
		return cmp.Compare(strings.ToLower(a.name), strings.ToLower(b.name))
	})
}

func initialsOfPeople(ps []crewPerson) []string {
	orderPeople(ps)
	out := make([]string, 0, maxInitials)
	for _, p := range ps {
		if in := initialsOf(p.name); in != "" {
			out = append(out, in)
		}
		if len(out) == maxInitials {
			break
		}
	}
	return out
}

// withCrew fills the repo, PR count and crew fields of the summaries of ls
// (out[i] is for ls[i]) with a fixed number of queries, whatever the number of launches.
func withCrew(ctx context.Context, d Deps, ls []sqlc.Launch, out []Summary) error {
	if len(ls) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(ls))
	var repoIDs, ownerIDs []uuid.UUID
	for i, l := range ls {
		ids[i] = l.ID
		if l.RepoID.Valid {
			repoIDs = append(repoIDs, l.RepoID.UUID)
		}
		if l.OwnerUserID.Valid {
			ownerIDs = append(ownerIDs, l.OwnerUserID.UUID)
		}
	}

	repos := map[uuid.UUID]string{}
	if len(repoIDs) > 0 {
		rows, err := d.Pool.Query(ctx, `SELECT id, full_name FROM repos WHERE id = ANY($1)`, repoIDs)
		if err != nil {
			return fmt.Errorf("launch: list repos: %w", err)
		}
		for rows.Next() {
			var id uuid.UUID
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				rows.Close()
				return err
			}
			repos[id] = name
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}

	// The captain's GitHub id per owner: the lowest, as load picks it.
	captainGH := map[uuid.UUID]int64{}
	if len(ownerIDs) > 0 {
		rows, err := d.Pool.Query(ctx, `SELECT user_id, github_user_id FROM github_identities WHERE user_id = ANY($1)`, ownerIDs)
		if err != nil {
			return fmt.Errorf("launch: list captain identities: %w", err)
		}
		for rows.Next() {
			var u uuid.UUID
			var gh int64
			if err := rows.Scan(&u, &gh); err != nil {
				rows.Close()
				return err
			}
			if cur, ok := captainGH[u]; !ok || gh < cur {
				captainGH[u] = gh
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}

	type member struct {
		state, role string
		gh          int64
	}
	byLaunch := map[uuid.UUID][]member{}
	var ghIDs []int64
	rows, err := d.Pool.Query(ctx, `SELECT launch_id, state, role, github_user_id FROM crew_members WHERE launch_id = ANY($1)`, ids)
	if err != nil {
		return fmt.Errorf("launch: list crew: %w", err)
	}
	for rows.Next() {
		var id uuid.UUID
		var m member
		var gh *int64
		if err := rows.Scan(&id, &m.state, &m.role, &gh); err != nil {
			rows.Close()
			return err
		}
		if gh != nil {
			m.gh = *gh
			ghIDs = append(ghIDs, m.gh)
		}
		byLaunch[id] = append(byLaunch[id], m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, gh := range captainGH {
		ghIDs = append(ghIDs, gh)
	}
	logins := map[int64]string{}
	if len(ghIDs) > 0 {
		rows, err := d.Pool.Query(ctx, `SELECT github_user_id, login FROM github_identities WHERE github_user_id = ANY($1)`, ghIDs)
		if err != nil {
			return fmt.Errorf("launch: list logins: %w", err)
		}
		for rows.Next() {
			var gh int64
			var login string
			if err := rows.Scan(&gh, &login); err != nil {
				rows.Close()
				return err
			}
			logins[gh] = login
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}

	for i, l := range ls {
		if l.RepoID.Valid {
			out[i].Repo = repos[l.RepoID.UUID]
		}
		changes, err := changesOf(l)
		if err != nil {
			changes = nil // a bad stored value should not hide the whole list
		}
		for _, c := range changes {
			if !c.Hidden {
				out[i].PRCount++
			}
		}
		captain := int64(0)
		if l.OwnerUserID.Valid {
			captain = captainGH[l.OwnerUserID.UUID]
		}
		// The captain's name as the detail page shows it: their login, else the repo owner.
		out[i].CaptainName = logins[captain]
		if out[i].CaptainName == "" && out[i].Repo != "" {
			out[i].CaptainName, _, _ = strings.Cut(out[i].Repo, "/")
		}
		var counts Counts
		var people []crewPerson
		for _, m := range byLaunch[l.ID] {
			if captain != 0 && m.gh == captain {
				continue // the captain is not their own crew
			}
			counts.add(m.state)
			if m.state == trigger.StateSuggested {
				continue
			}
			login := ""
			for _, c := range changes {
				if m.gh != 0 && c.AuthorID == m.gh && c.AuthorLogin != "" {
					login = c.AuthorLogin
					break
				}
			}
			if login == "" {
				login = logins[m.gh]
			}
			people = append(people, crewPerson{name: firstNonEmpty(login, m.role), in: m.state == "queued" || m.state == "posted" || m.state == "copied"})
		}
		out[i].CrewIn, out[i].CrewTotal = counts.In, counts.Total
		out[i].CrewInitials = initialsOfPeople(people)
	}
	return nil
}
