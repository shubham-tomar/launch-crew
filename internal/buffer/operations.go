package buffer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
)

// Channel is a connected social account. Type is Buffer's channel type
// (profile, page, business, account, group or channel), lowercase.
type Channel struct {
	ID, Name, Service, Avatar string
	Type                      string
	Locked, Paused, Connected bool
}

// RemindersOnly reports a channel Buffer cannot publish to by itself: an
// Instagram personal profile, which only takes reminder (notification) posts.
func RemindersOnly(ch Channel) bool {
	return strings.ToLower(ch.Service) == "instagram" && strings.ToLower(ch.Type) == "profile"
}

// ServiceRules is what a service accepts for a regular post.
type ServiceRules struct {
	MaxChars                       int
	Threads, ImageOK, FirstComment bool
	// MaxImages is how many images one post takes; 0 when ImageOK is false.
	// MaxImageBytes is the network's per-image size that Buffer cannot
	// resize down to (Bluesky); 0 where Buffer makes a smaller copy itself.
	// Buffer's configuration query reports neither, so both always come
	// from the built-in table.
	MaxImages     int
	MaxImageBytes int
}

// Config holds rules keyed by lowercase Buffer service name (twitter, bluesky, ...).
// Fallback is set when Buffer's configuration query failed and the rules are
// the built-in table; such a config is cached for fallbackTTL, not cacheTTL.
type Config struct {
	Services map[string]ServiceRules
	Fallback bool `json:",omitempty"`
}

// Post is a Buffer post. ExternalLink is its URL on the network once sent.
type Post struct {
	ID, ChannelID, Text, Status string
	ExternalLink                string
	SentAt, CreatedAt           time.Time
}

// CreatePostInput describes one post. Mode is addToQueue or customScheduled
// (DueAt required). Thread, when set, is the whole thread in order, root
// first; Text defaults to Thread[0]. Notification asks Buffer to remind the
// person to publish by hand instead of publishing itself, which is what an
// Instagram personal profile needs.
type CreatePostInput struct {
	ChannelID, Text string
	Mode            string
	DueAt           *time.Time
	SaveToDraft     bool
	Notification    bool
	AssetURLs       []string
	FirstComment    string
	Thread          []string
	// Story sends an Instagram or Facebook post as a story; elsewhere it is
	// ignored. A story shows its images, not its text.
	Story bool
	// Document, when set, replaces AssetURLs with one PDF, which LinkedIn
	// shows as a swipeable document.
	Document *Document
}

// Document is a PDF attachment, the title the network shows with it, and
// the image shown before it opens. Buffer refuses a document missing any of
// the three.
type Document struct {
	URL, Title, ThumbnailURL string
}

// Post formats a channel can choose beyond a plain post.
const (
	// FormatStory posts to an Instagram or Facebook story.
	FormatStory = "story"
	// FormatDocument posts the images to LinkedIn as one swipeable PDF.
	FormatDocument = "document"
)

// FormatAllowed reports whether a channel on service can post as format;
// the empty format, a plain post, is always allowed.
func FormatAllowed(service, format string) bool {
	switch format {
	case "":
		return true
	case FormatStory:
		s := strings.ToLower(service)
		return s == "instagram" || s == "facebook"
	case FormatDocument:
		return strings.ToLower(service) == "linkedin"
	}
	return false
}

// CreatePostResult reports the created post. Notification is set when Buffer
// will remind the person to publish it rather than post it for them.
type CreatePostResult struct {
	PostID       string
	SavedAsDraft bool
	Notification bool
	Note         string
}

// NotifyNote is the note on a post Buffer will send a reminder for.
const NotifyNote = "Buffer will notify you to post this one yourself"

// Limit is one channel's daily posting limit status. Limit nil means unlimited.
type Limit struct {
	IsAtLimit       bool
	Limit           *int
	Scheduled, Sent int
}

func (c *Client) fresh(at pgtype.Timestamptz) bool {
	return c.freshFor(at, cacheTTL)
}

func (c *Client) freshFor(at pgtype.Timestamptz, ttl time.Duration) bool {
	return at.Valid && c.now().Sub(at.Time) < ttl
}

func orgID(conn sqlc.BufferConnection) (string, error) {
	if !conn.OrgID.Valid || conn.OrgID.String == "" {
		return "", errors.New("buffer: connection has no organization id")
	}
	return conn.OrgID.String, nil
}

// Channels lists the organization's channels, cached on the connection for 1 h.
func (c *Client) Channels(ctx context.Context, connID uuid.UUID) ([]Channel, error) {
	conn, err := c.q.GetBufferConnection(ctx, connID)
	if err != nil {
		return nil, fmt.Errorf("buffer: load connection: %w", err)
	}
	if c.fresh(conn.ChannelsCachedAt) && conn.ChannelsJson != nil {
		var chs []Channel
		if err := json.Unmarshal(conn.ChannelsJson, &chs); err == nil {
			return chs, nil
		}
	}
	org, err := orgID(conn)
	if err != nil {
		return nil, err
	}
	var out struct {
		Channels []struct {
			ID, Name, Service, Avatar, Type         string
			DisplayName                             *string
			IsLocked, IsQueuePaused, IsDisconnected bool
		}
	}
	if err := c.Do(ctx, connID, qChannels, map[string]any{"organizationId": org}, &out); err != nil {
		return nil, err
	}
	chs := make([]Channel, 0, len(out.Channels))
	for _, ch := range out.Channels {
		name := ch.Name
		if ch.DisplayName != nil && *ch.DisplayName != "" {
			name = *ch.DisplayName
		}
		chs = append(chs, Channel{
			ID: ch.ID, Name: name, Service: strings.ToLower(ch.Service), Avatar: ch.Avatar, Type: strings.ToLower(ch.Type),
			Locked: ch.IsLocked, Paused: ch.IsQueuePaused, Connected: !ch.IsDisconnected,
		})
	}
	c.saveCache(ctx, connID, chs, func(b []byte, at pgtype.Timestamptz) error {
		return c.q.UpdateConnectionChannelsCache(ctx, sqlc.UpdateConnectionChannelsCacheParams{ID: connID, ChannelsJson: b, ChannelsCachedAt: at})
	})
	return chs, nil
}

// RefreshChannels drops the cached channel list and reads it again from
// Buffer. A cache under refreshFloor old is kept, so repeated calls do not
// spend Buffer's rate budget.
func (c *Client) RefreshChannels(ctx context.Context, connID uuid.UUID) ([]Channel, error) {
	conn, err := c.q.GetBufferConnection(ctx, connID)
	if err != nil {
		return nil, fmt.Errorf("buffer: load connection: %w", err)
	}
	if !conn.ChannelsCachedAt.Valid || c.now().Sub(conn.ChannelsCachedAt.Time) >= refreshFloor {
		if err := c.q.UpdateConnectionChannelsCache(ctx, sqlc.UpdateConnectionChannelsCacheParams{ID: connID}); err != nil {
			return nil, fmt.Errorf("buffer: clear channel cache: %w", err)
		}
	}
	return c.Channels(ctx, connID)
}

func (c *Client) saveCache(ctx context.Context, connID uuid.UUID, v any, save func([]byte, pgtype.Timestamptz) error) {
	b, err := json.Marshal(v)
	if err == nil {
		err = save(b, pgtype.Timestamptz{Time: c.now(), Valid: true})
	}
	if err != nil {
		log.Printf("buffer: cache for %s: %v", connID, err)
	}
}

// builtinRules come from Buffer's character-limits and posts guides
// (2026-11-01) and its media guides (images per post and sizes, checked
// 2026-11-09). They fill whatever the configuration query leaves out.
// Buffer refuses files over 10 MB and makes a smaller copy for networks
// with lower limits, except Bluesky's 1 MB, which it cannot shrink to.
var builtinRules = map[string]ServiceRules{
	"twitter":        {MaxChars: 280, Threads: true, ImageOK: true, MaxImages: 4},
	"bluesky":        {MaxChars: 300, Threads: true, ImageOK: true, MaxImages: 4, MaxImageBytes: 1_000_000},
	"threads":        {MaxChars: 500, Threads: true, ImageOK: true, MaxImages: 4},
	"mastodon":       {MaxChars: 500, Threads: true, ImageOK: true, MaxImages: 4},
	"linkedin":       {MaxChars: 3000, ImageOK: true, FirstComment: true, MaxImages: 20},
	"facebook":       {MaxChars: 5000, ImageOK: true, FirstComment: true, MaxImages: 10},
	"instagram":      {MaxChars: 2196, ImageOK: true, FirstComment: true, MaxImages: 20},
	"youtube":        {MaxChars: 5000},
	"tiktok":         {MaxChars: 2200, ImageOK: true, MaxImages: 10},
	"pinterest":      {MaxChars: 500, ImageOK: true, MaxImages: 1},
	"googlebusiness": {MaxChars: 4000, ImageOK: true, MaxImages: 1},
}

type serviceConfig struct {
	Service string
	Content []struct {
		ConfigurationContentTypes []string
		SupportedProperties       []string
		Rules                     []struct {
			Typename  string `json:"__typename"`
			Property  string
			MaxLength int
		}
	}
}

// Configuration returns per-service post rules, cached on the connection for 1 h.
// If Buffer's (experimental) configuration query errors, it returns the
// built-in table, cached for 15 min so callers do not retry Buffer on every
// page load.
func (c *Client) Configuration(ctx context.Context, connID uuid.UUID) (Config, error) {
	conn, err := c.q.GetBufferConnection(ctx, connID)
	if err != nil {
		return Config{}, fmt.Errorf("buffer: load connection: %w", err)
	}
	if conn.ConfigJson != nil {
		var cfg Config
		if err := json.Unmarshal(conn.ConfigJson, &cfg); err == nil {
			ttl := cacheTTL
			if cfg.Fallback {
				ttl = fallbackTTL
			}
			if c.freshFor(conn.ConfigCachedAt, ttl) {
				return cfg, nil
			}
		}
	}
	org, err := orgID(conn)
	if err != nil {
		return Config{}, err
	}
	var out struct {
		Configuration struct{ Services []serviceConfig }
	}
	err = c.Do(ctx, connID, qConfiguration, map[string]any{"organizationId": org}, &out)
	var cfg Config
	var gqlErr *GraphQLError
	switch {
	case errors.As(err, &gqlErr):
		log.Printf("buffer: configuration unavailable, using built-in rules: %v", err)
		cfg = buildConfig(nil)
		cfg.Fallback = true
	case err != nil:
		return Config{}, err
	default:
		cfg = buildConfig(out.Configuration.Services)
	}
	c.saveCache(ctx, connID, cfg, func(b []byte, at pgtype.Timestamptz) error {
		return c.q.UpdateConnectionConfigCache(ctx, sqlc.UpdateConnectionConfigCacheParams{ID: connID, ConfigJson: b, ConfigCachedAt: at})
	})
	return cfg, nil
}

// buildConfig reads the "post" content entry of each service. A service with
// several channel types gets the strictest combination (lowest MaxChars, a
// capability only if every type has it). Missing services and lengths come
// from builtinRules.
func buildConfig(services []serviceConfig) Config {
	got := map[string]ServiceRules{}
	for _, s := range services {
		name := strings.ToLower(s.Service)
		for _, ct := range s.Content {
			if !slices.Contains(ct.ConfigurationContentTypes, "post") {
				continue
			}
			r := ServiceRules{
				Threads:      slices.Contains(ct.SupportedProperties, "thread"),
				ImageOK:      slices.Contains(ct.SupportedProperties, "image"),
				FirstComment: slices.Contains(ct.SupportedProperties, "firstComment"),
			}
			for _, rule := range ct.Rules {
				if rule.Typename == "LengthRule" && rule.Property == "text" {
					r.MaxChars = rule.MaxLength
				}
			}
			prev, seen := got[name]
			if seen {
				if prev.MaxChars > 0 && (r.MaxChars == 0 || prev.MaxChars < r.MaxChars) {
					r.MaxChars = prev.MaxChars
				}
				r.Threads = r.Threads && prev.Threads
				r.ImageOK = r.ImageOK && prev.ImageOK
				r.FirstComment = r.FirstComment && prev.FirstComment
			}
			got[name] = r
		}
	}
	for name, b := range builtinRules {
		r, ok := got[name]
		if !ok {
			got[name] = b
			continue
		}
		if r.MaxChars == 0 {
			r.MaxChars = b.MaxChars
		}
		if r.ImageOK {
			r.MaxImages, r.MaxImageBytes = b.MaxImages, b.MaxImageBytes
		}
		got[name] = r
	}
	return Config{Services: got}
}

type postNode struct {
	ID, ChannelID, Text, Status string
	ExternalLink                *string
	SentAt                      *time.Time
	CreatedAt                   time.Time
}

type postsData struct {
	Posts struct {
		Edges []struct{ Node postNode }
	}
}

func (d postsData) posts() []Post {
	out := make([]Post, 0, len(d.Posts.Edges))
	for _, e := range d.Posts.Edges {
		p := Post{ID: e.Node.ID, ChannelID: e.Node.ChannelID, Text: e.Node.Text, Status: e.Node.Status, CreatedAt: e.Node.CreatedAt}
		if e.Node.ExternalLink != nil {
			p.ExternalLink = *e.Node.ExternalLink
		}
		if e.Node.SentAt != nil {
			p.SentAt = *e.Node.SentAt
		}
		out = append(out, p)
	}
	return out
}

// SentPosts returns up to limit (max 100) sent posts, newest first, in one request.
func (c *Client) SentPosts(ctx context.Context, connID uuid.UUID, limit int) ([]Post, error) {
	if limit <= 0 || limit > maxPage {
		limit = maxPage
	}
	conn, err := c.q.GetBufferConnection(ctx, connID)
	if err != nil {
		return nil, fmt.Errorf("buffer: load connection: %w", err)
	}
	org, err := orgID(conn)
	if err != nil {
		return nil, err
	}
	var out postsData
	if err := c.Do(ctx, connID, qSentPosts, map[string]any{"organizationId": org, "first": limit}, &out); err != nil {
		return nil, err
	}
	return out.posts(), nil
}

// PostsCreatedBetween returns the first 100 posts on a channel created in [from, to].
func (c *Client) PostsCreatedBetween(ctx context.Context, connID uuid.UUID, channelID string, from, to time.Time) ([]Post, error) {
	conn, err := c.q.GetBufferConnection(ctx, connID)
	if err != nil {
		return nil, fmt.Errorf("buffer: load connection: %w", err)
	}
	org, err := orgID(conn)
	if err != nil {
		return nil, err
	}
	var out postsData
	err = c.Do(ctx, connID, qPostsCreatedBetween, map[string]any{
		"organizationId": org, "channelId": channelID, "first": maxPage,
		"start": from.UTC().Format(time.RFC3339Nano), "end": to.UTC().Format(time.RFC3339Nano),
	}, &out)
	if err != nil {
		return nil, err
	}
	return out.posts(), nil
}

// CreatePost creates a post. If Buffer answers LimitReachedError, it retries
// once as a draft and says so in Note. If Buffer refuses because the channel
// only takes reminder posts (an Instagram personal profile the channel list
// did not flag), it retries once as a notification post and says so in Note.
func (c *Client) CreatePost(ctx context.Context, connID uuid.UUID, in CreatePostInput) (CreatePostResult, error) {
	input, err := c.postInput(ctx, connID, in)
	if err != nil {
		return CreatePostResult{}, err
	}
	res := CreatePostResult{SavedAsDraft: in.SaveToDraft, Notification: in.Notification}
	if in.Notification {
		res.Note = NotifyNote
	}
	id, err := c.createPost(ctx, connID, input)
	var me *MutationError
	if errors.As(err, &me) && !in.Notification && wantsReminder(me.Message) {
		input["schedulingType"] = "notification"
		res.Notification, res.Note = true, NotifyNote
		id, err = c.createPost(ctx, connID, input)
	}
	var lre *LimitReachedError
	if errors.As(err, &lre) && !in.SaveToDraft {
		input["saveToDraft"] = true
		if id, err = c.createPost(ctx, connID, input); err != nil {
			return CreatePostResult{}, err
		}
		res.PostID, res.SavedAsDraft, res.Note = id, true, DraftNote
		return res, nil
	}
	if err != nil {
		return CreatePostResult{}, err
	}
	res.PostID = id
	return res, nil
}

// wantsReminder reports a refusal that says the channel cannot publish by
// itself and needs a reminder (notification) post instead.
func wantsReminder(msg string) bool {
	m := strings.ToLower(msg)
	for _, w := range []string{"notification", "reminder", "personal"} {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}

func (c *Client) createPost(ctx context.Context, connID uuid.UUID, input map[string]any) (string, error) {
	var out struct {
		CreatePost struct {
			Typename string `json:"__typename"`
			Message  string
			Post     struct{ ID string }
		}
	}
	if err := c.Do(ctx, connID, qCreatePost, map[string]any{"input": input}, &out); err != nil {
		return "", err
	}
	r := out.CreatePost
	switch r.Typename {
	case "PostActionSuccess":
		return r.Post.ID, nil
	case "LimitReachedError":
		return "", &LimitReachedError{Message: r.Message}
	default:
		return "", &MutationError{Type: r.Typename, Message: r.Message}
	}
}

// metadataKeys maps a channel service to its PostInputMetaData field.
var metadataKeys = map[string]string{"googlebusiness": "google"}

var videoExts = []string{".mp4", ".mov", ".m4v", ".webm"}

func (c *Client) postInput(ctx context.Context, connID uuid.UUID, in CreatePostInput) (map[string]any, error) {
	text := in.Text
	if text == "" && len(in.Thread) > 0 {
		text = in.Thread[0]
	}
	mode := in.Mode
	if mode == "" {
		mode = "addToQueue"
	}
	scheduling := "automatic"
	if in.Notification {
		scheduling = "notification"
	}
	input := map[string]any{
		"channelId": in.ChannelID, "text": text,
		"schedulingType": scheduling, "mode": mode,
	}
	switch mode {
	case "addToQueue":
	case "customScheduled":
		if in.DueAt == nil {
			return nil, errors.New("buffer: customScheduled needs DueAt")
		}
		input["dueAt"] = in.DueAt.UTC().Format(time.RFC3339)
	default:
		return nil, fmt.Errorf("buffer: unknown mode %q", mode)
	}
	if in.SaveToDraft {
		input["saveToDraft"] = true
	}
	var assets []map[string]any
	if in.Document != nil {
		assets = append(assets, map[string]any{"document": map[string]any{"url": in.Document.URL, "title": in.Document.Title, "thumbnailUrl": in.Document.ThumbnailURL}})
	}
	for _, u := range in.AssetURLs {
		if in.Document != nil {
			break
		}
		kind := "image"
		if slices.Contains(videoExts, strings.ToLower(path.Ext(strings.SplitN(u, "?", 2)[0]))) {
			kind = "video"
		}
		assets = append(assets, map[string]any{kind: map[string]any{"url": u}})
	}
	// For a thread Buffer reads the thread items, not the top-level fields,
	// so the card goes on the root item; a top-level copy is dropped silently.
	if len(assets) > 0 && len(in.Thread) == 0 {
		input["assets"] = assets
	}
	// The channel's service picks the metadata key. It comes from the cached
	// channel list; when that cannot say, a post that needs no metadata goes
	// out without it rather than failing here.
	needs := in.FirstComment != "" || len(in.Thread) > 0
	svc, err := c.channelService(ctx, connID, in.ChannelID)
	if err != nil && needs {
		return nil, err
	}
	// Buffer refuses an Instagram post without a type ("Instagram posts
	// require a type (post, story, or reel)"). Ours are feed posts; several
	// images in one make a carousel, as in Buffer's composer.
	instagram := strings.ToLower(svc) == "instagram"
	// A Facebook post goes out as Buffer's default unless it is a story.
	fbStory := in.Story && strings.ToLower(svc) == "facebook"
	if needs || instagram || fbStory {
		key := svc
		if k, ok := metadataKeys[svc]; ok {
			key = k
		}
		md := map[string]any{}
		switch {
		case instagram && in.Story:
			md["type"] = FormatStory
			md["shouldShareToFeed"] = false
		case instagram:
			md["type"] = "post"
			md["shouldShareToFeed"] = true
		case fbStory:
			md["type"] = FormatStory
		}
		if in.FirstComment != "" {
			md["firstComment"] = in.FirstComment
		}
		if len(in.Thread) > 0 {
			items := make([]map[string]any, 0, len(in.Thread))
			for i, t := range in.Thread {
				item := map[string]any{"text": t}
				if i == 0 && len(assets) > 0 {
					item["assets"] = assets
				}
				items = append(items, item)
			}
			md["thread"] = items
		}
		input["metadata"] = map[string]any{key: md}
	}
	return input, nil
}

func (c *Client) channelService(ctx context.Context, connID uuid.UUID, channelID string) (string, error) {
	chs, err := c.Channels(ctx, connID)
	if err != nil {
		return "", err
	}
	for _, ch := range chs {
		if ch.ID == channelID {
			return ch.Service, nil
		}
	}
	return "", fmt.Errorf("buffer: unknown channel %q", channelID)
}

// DailyPostingLimits returns today's posting limit status for every unlocked
// channel. The answer is kept in memory for a minute, so the captain page
// polling preflight does not ask Buffer every time.
func (c *Client) DailyPostingLimits(ctx context.Context, connID uuid.UUID) (map[string]Limit, error) {
	if cached, ok := c.cachedLimits(connID); ok {
		return cached, nil
	}
	chs, err := c.Channels(ctx, connID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, ch := range chs {
		if !ch.Locked {
			ids = append(ids, ch.ID)
		}
	}
	limits := map[string]Limit{}
	if len(ids) == 0 {
		return limits, nil
	}
	var out struct {
		DailyPostingLimits []struct {
			ChannelID       string
			IsAtLimit       bool
			Limit           *int
			Scheduled, Sent int
		}
	}
	if err := c.Do(ctx, connID, qDailyPostingLimits, map[string]any{"channelIds": ids}, &out); err != nil {
		return nil, err
	}
	for _, l := range out.DailyPostingLimits {
		limits[l.ChannelID] = Limit{IsAtLimit: l.IsAtLimit, Limit: l.Limit, Scheduled: l.Scheduled, Sent: l.Sent}
	}
	c.mu.Lock()
	c.limits[connID] = limitsEntry{at: c.now(), limits: limits}
	c.mu.Unlock()
	return maps.Clone(limits), nil
}

func (c *Client) cachedLimits(connID uuid.UUID) (map[string]Limit, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.limits[connID]
	if !ok || c.now().Sub(e.at) >= limitsTTL {
		return nil, false
	}
	return maps.Clone(e.limits), true
}
