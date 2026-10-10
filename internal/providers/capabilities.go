package providers

import (
	"path"
	"strings"
	"sync"
	"unique"
)

var (
	capsCacheMu sync.RWMutex
	capsCache   = make(map[unique.Handle[string]]Capabilities)
)

var (
	customCapsMu sync.RWMutex
	customCaps   = map[string]Capabilities{}
)

// InvalidateCapabilitiesCache resets the cached model capabilities.
func InvalidateCapabilitiesCache() {
	capsCacheMu.Lock()
	capsCache = make(map[unique.Handle[string]]Capabilities)
	capsCacheMu.Unlock()
}

// SetCustomModelCaps registers caps for a custom model (provider/model).
func SetCustomModelCaps(provider, model string, caps Capabilities) {
	key := provider + "||" + model
	customCapsMu.Lock()
	customCaps[key] = caps
	// also store base model variant
	base := model
	if i := strings.LastIndex(model, "/"); i >= 0 {
		base = model[i+1:]
	}
	if base != model {
		customCaps[provider+"||"+base] = caps
	}
	customCapsMu.Unlock()
	// Invalidate cache so new caps are picked up
	InvalidateCapabilitiesCache()
}

// GetCustomModelCaps returns custom caps if present.
func GetCustomModelCaps(provider, model string) (Capabilities, bool) {
	key := provider + "||" + model
	customCapsMu.RLock()
	caps, ok := customCaps[key]
	customCapsMu.RUnlock()
	if ok {
		return caps, true
	}
	// try base model
	if i := strings.LastIndex(model, "/"); i >= 0 {
		base := model[i+1:]
		customCapsMu.RLock()
		caps, ok = customCaps[provider+"||"+base]
		customCapsMu.RUnlock()
		return caps, ok
	}
	return Capabilities{}, false
}

// ClearCustomModelCaps clears all custom caps (for tests).
func ClearCustomModelCaps() {
	customCapsMu.Lock()
	customCaps = map[string]Capabilities{}
	customCapsMu.Unlock()
	InvalidateCapabilitiesCache()
}

// Capabilities represents what a model can do beyond plain text.
type Capabilities struct {
	Vision      bool
	PDF         bool
	AudioInput  bool
	VideoInput  bool
	ImageOutput bool
	AudioOutput bool
	Search      bool
	Tools       bool
	Reasoning   bool
}

var DefaultCapabilities = Capabilities{
	Vision:      false,
	PDF:         false,
	AudioInput:  false,
	VideoInput:  false,
	ImageOutput: false,
	AudioOutput: false,
	Search:      false,
	Tools:       true,
	Reasoning:   false,
}

var modelCapabilities = map[string]Capabilities{
	"claude-opus-5":                    {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-5-thinking":           {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-5-agentic":            {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-5-thinking-agentic":   {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-4.6":                  {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-4.7":                  {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-4-7":                  {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-4.8":                  {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-4-6":                  {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-4-8":                  {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-4.8-thinking":         {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-opus-4-8-thinking":         {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-sonnet-4.6":                {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-sonnet-4-6":                {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-sonnet-5":                  {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-sonnet-5-thinking":         {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-sonnet-5-agentic":          {Vision: true, Reasoning: true, Search: true, Tools: true},
	"claude-sonnet-5-thinking-agentic": {Vision: true, Reasoning: true, Search: true, Tools: true},
	"gpt-6-astra":                      {Vision: true, Reasoning: true, Search: true, Tools: true},
	"gpt-5.6-sol-image":                {ImageOutput: true, Tools: true},
	"gpt-5.6-terra-image":              {ImageOutput: true, Tools: true},
	"gpt-5.6-luna-image":               {ImageOutput: true, Tools: true},
	"gpt-image-1":                      {ImageOutput: true},
	"glm-5.3-flash":                    {Vision: true, Reasoning: true, Tools: true},
	"glm-5.3":                          {Reasoning: true, Tools: true},
	"glm-4.6v":                         {Vision: true, Reasoning: true, Tools: true},
	"deepseek-v4-vision":               {Vision: true, Reasoning: true, Tools: true},
	"deepseek-v4.1-flash":              {Vision: true, Reasoning: true, Tools: true},
	"deepseek-flash":                   {Vision: true, Reasoning: true, Tools: true},
	"grok-4.6":                         {Vision: true, Reasoning: true, Search: true, Tools: true},
	"grok-4.5":                         {Vision: true, Reasoning: true, Search: true, Tools: true},
	"muse-spark-1.2-contributor-free":  {Vision: true, Reasoning: true, Tools: true},
	"muse-spark-1.3-contributor-free":  {Vision: true, Reasoning: true, Tools: true},
	"union-alpha":                      {Reasoning: true, Tools: true},
	"vision-model":                     {Vision: true, Reasoning: true, Tools: true},
	"coder-model":                      {Reasoning: true, Tools: true},
	"kimi-k3":                          {Vision: true, VideoInput: true, Reasoning: true, Tools: true},
	"k3":                               {Vision: true, VideoInput: true, Reasoning: true, Tools: true},
	"kimi-for-coding":                  {Vision: true, VideoInput: true, Reasoning: true, Tools: true},
	"kimi-for-coding-highspeed":        {Vision: true, VideoInput: true, Reasoning: true, Tools: true},
	"kimi-k2.7-code":                   {Vision: true, VideoInput: true, Reasoning: true, Tools: true},
	"kimi-k2.7-code-highspeed":         {Vision: true, VideoInput: true, Reasoning: true, Tools: true},
}

var providerCapabilities = map[string]map[string]Capabilities{
	"nvidia": {
		"minimaxai/minimax-m2.7":        {Reasoning: true, Tools: true},
		"minimaxai/minimax-m3":          {Vision: true, Reasoning: true, Tools: true},
		"z-ai/glm-5.2":                  {Reasoning: true, Tools: true},
		"deepseek-ai/deepseek-v4-pro":   {Reasoning: true, Tools: true},
		"deepseek-ai/deepseek-v4-flash": {Reasoning: true, Tools: true},
	},
	"codex": {
		"gpt-6-astra":            {Vision: true, Reasoning: true, Search: true, Tools: true},
		"gpt-5.6-sol":            {Vision: true, Reasoning: true, Search: true, Tools: true},
		"gpt-5.6-sol-review":     {Vision: true, Reasoning: true, Search: true, Tools: true},
		"gpt-5.6-terra":          {Vision: true, Reasoning: true, Search: true, Tools: true},
		"gpt-5.6-terra-review":   {Vision: true, Reasoning: true, Search: true, Tools: true},
		"gpt-5.6-luna":           {Vision: true, Reasoning: true, Search: true, Tools: true},
		"gpt-5.6-luna-review":    {Vision: true, Reasoning: true, Search: true, Tools: true},
		"gpt-5.6-sol-image":      {ImageOutput: true, Tools: true},
		"gpt-5.6-terra-image":    {ImageOutput: true, Tools: true},
		"gpt-5.6-luna-image":     {ImageOutput: true, Tools: true},
		"gpt-image-2.5":          {ImageOutput: true, Tools: true},
		"gpt-image-2.5-flare":    {ImageOutput: true, Tools: true},
		"gpt-image-2.5-sunburst": {ImageOutput: true, Tools: true},
		"gpt-image-2":            {ImageOutput: true, Tools: true},
		"gpt-image-1.5":          {ImageOutput: true, Tools: true},
	},
	"codebuddy-cn": {
		"glm-5.2":             {Vision: true, Reasoning: true, Tools: true},
		"glm-5.1":             {Vision: true, Reasoning: true, Tools: true},
		"glm-5.0-turbo":       {Reasoning: true, Tools: true},
		"glm-5v-turbo":        {Vision: true, Reasoning: true, Tools: true},
		"minimax-m3":          {Vision: true, Reasoning: true, Tools: true},
		"minimax-m2.7":        {Vision: true, Reasoning: true, Tools: true},
		"kimi-k2.7":           {Vision: true, Reasoning: true, Tools: true},
		"kimi-k2.6":           {Vision: true, Reasoning: true, Tools: true},
		"kimi-k2.5":           {Vision: true, Reasoning: true, Tools: true},
		"hy3-preview":         {Vision: true, Reasoning: true, Tools: true},
		"hy3":                 {Vision: true, Reasoning: true, Tools: true},
		"hy3-x":               {Vision: true, Reasoning: true, Tools: true},
		"hy4-preview":         {Vision: true, Reasoning: true, Tools: true},
		"hy4-preview-x":       {Vision: true, Reasoning: true, Tools: true},
		"glm-5.3":             {Vision: true, Reasoning: true, Tools: true},
		"glm-5.3-flash":       {Vision: true, Reasoning: true, Tools: true},
		"kimi-k3-1":           {Vision: true, Reasoning: true, Tools: true},
		"deepseek-v4-pro":     {Vision: true, Reasoning: true, Tools: true},
		"deepseek-v4.1-flash": {Vision: true, Reasoning: true, Tools: true},
		"deepseek-v4-flash":   {Vision: true, Reasoning: true, Tools: true},
		"deepseek-v3-2-volc":  {Reasoning: true, Tools: true},
	},
	"qoder": {
		"ultimate":      {Vision: true, Reasoning: true, Tools: true},
		"performance":   {Vision: true, Reasoning: true, Tools: true},
		"dmodel":        {Reasoning: true, Tools: true},
		"dfmodel":       {Reasoning: true, Tools: true},
		"gmodel":        {Reasoning: true, Tools: true},
		"gfmodel":       {Vision: true, Reasoning: true, Tools: true},
		"kmodel_latest": {Vision: true, Reasoning: true, Tools: true},
		"kmodel":        {Vision: true, Reasoning: true, Tools: true},
		"mmodel":        {Reasoning: true, Tools: true},
		"qmodel_latest": {Vision: true, Reasoning: true, Tools: true},
		"qmodel":        {Vision: true, Reasoning: true, Tools: true},
		"qfmodel":       {Vision: true, Reasoning: true, Tools: true},
		"qmodel_38max":  {Vision: true, Reasoning: true, Tools: true},
	},
	"poolside": {
		"laguna-s-2.1":  {Reasoning: true, Tools: true},
		"laguna-xs-2.1": {Reasoning: true, Tools: true},
	},
}

func init() {
	kiroGpt56 := Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}
	providerCapabilities["kiro"] = map[string]Capabilities{
		"gpt-5.6-sol":                    kiroGpt56,
		"gpt-5.6-terra":                  kiroGpt56,
		"gpt-5.6-luna":                   kiroGpt56,
		"gpt-5.6-sol-thinking":           kiroGpt56,
		"gpt-5.6-terra-thinking":         kiroGpt56,
		"gpt-5.6-luna-thinking":          kiroGpt56,
		"gpt-5.6-sol-agentic":            kiroGpt56,
		"gpt-5.6-terra-agentic":          kiroGpt56,
		"gpt-5.6-luna-agentic":           kiroGpt56,
		"gpt-5.6-sol-thinking-agentic":   kiroGpt56,
		"gpt-5.6-terra-thinking-agentic": kiroGpt56,
		"gpt-5.6-luna-thinking-agentic":  kiroGpt56,
	}
}

type patternCapability struct {
	pattern string
	caps    Capabilities
}

var patternCapabilities = []patternCapability{
	{"*claude*opus-5*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*opus-4.6*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*opus-4.7*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*opus-4.8*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*sonnet-4.6*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*sonnet-4.7*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*haiku*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*opus*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*sonnet*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*fable*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude*mythos*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*claude-3*", Capabilities{Vision: true, Tools: true}},
	{"*claude*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},

	{"*gemini*image*", Capabilities{Vision: true, ImageOutput: true, Tools: true}},
	{"*gemini-3.8*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true}},
	{"*gemini-3*pro*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true}},
	{"*gemini-3*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true}},
	{"*gemini-2.5*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Search: true, Tools: true}},
	{"*gemini-2*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Search: true, Tools: true}},
	{"*gemini*", Capabilities{Vision: true, Search: true, Tools: true}},
	{"*gemma*", Capabilities{Vision: true, Tools: true}},
	{"*nanobanana*", Capabilities{Vision: true, ImageOutput: true, Tools: true}},

	{"*gpt-6*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},

	{"*gpt-5*image*", Capabilities{ImageOutput: true, Tools: true}},
	{"*gpt-image*", Capabilities{ImageOutput: true, Tools: true}},
	{"*gpt-5*codex*", Capabilities{Reasoning: true, Search: true, Tools: true}},
	{"*gpt-5*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*gpt-4o*", Capabilities{Vision: true, Search: true, Tools: true}},
	{"*gpt-4.1*", Capabilities{Vision: true, Tools: true}},
	{"*gpt-4-turbo*", Capabilities{Vision: true, Tools: true}},
	{"*gpt-4*", Capabilities{Tools: true}},
	{"*gpt-3.5*", Capabilities{Tools: true}},
	{"*gpt-oss*", Capabilities{Reasoning: true, Tools: true}},
	{"*solar-pro*", Capabilities{Reasoning: true, Tools: true}},
	{"*longcat*", Capabilities{Reasoning: true, Tools: true}},

	{"*o1-mini*", Capabilities{Reasoning: true, Tools: true}},
	{"*o1*", Capabilities{Vision: true, Reasoning: true, Tools: true}},
	{"*o3*", Capabilities{Vision: true, Reasoning: true, Tools: true}},
	{"*o4*", Capabilities{Vision: true, Reasoning: true, Tools: true}},

	{"*grok*image*", Capabilities{ImageOutput: true, Tools: true}},
	{"*grok-code*", Capabilities{Reasoning: true, Tools: true}},
	{"*grok-4.5*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*grok-4*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*grok-3*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},
	{"*grok*", Capabilities{Vision: true, Reasoning: true, Search: true, Tools: true}},

	{"*qwen*vl*", Capabilities{Vision: true, Reasoning: true, Tools: true}},
	{"*qwen*omni*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Reasoning: true, Tools: true}},
	{"*qwen*coder*", Capabilities{Reasoning: true, Tools: true}},
	{"*qwen*max*", Capabilities{Reasoning: true, Tools: true}},
	{"*qwen3.5*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true}},
	{"*qwen3.6*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true}},
	{"*qwen3.7*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true}},
	{"*qwen3.8*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true}},
	{"*qwen*plus*", Capabilities{Vision: true, Reasoning: true, Tools: true}},
	{"*qwen*235b*", Capabilities{Reasoning: true, Tools: true}},
	{"*qwq*", Capabilities{Reasoning: true, Tools: true}},
	{"*qwen*", Capabilities{Reasoning: true, Tools: true}},

	{"*kimi*k3*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true}},
	{"*kimi*for-coding*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true}},
	{"*kimi*k2.7*code*", Capabilities{Vision: true, VideoInput: true, Reasoning: true, Tools: true}},
	{"*kimi*k2*", Capabilities{Vision: true, Reasoning: true, Tools: true}},
	{"*kimi*", Capabilities{Reasoning: true, Tools: true}},

	{"*glm-5*", Capabilities{Reasoning: true, Tools: true}},
	{"*glm-4.7*", Capabilities{Reasoning: true, Tools: true}},
	{"*glm-4*", Capabilities{Reasoning: true, Tools: true}},
	{"*glm*", Capabilities{Reasoning: true, Tools: true}},
	{"*z-ai*", Capabilities{Reasoning: true, Tools: true}},
	{"*zai*", Capabilities{Reasoning: true, Tools: true}},

	{"*deepseek-v4*", Capabilities{Reasoning: true, Tools: true}},
	{"*deepseek*flash*", Capabilities{Vision: true, Reasoning: true, Tools: true}},
	{"*reasoner*", Capabilities{Reasoning: true, Tools: true}},
	{"*deepseek-r*", Capabilities{Reasoning: true, Tools: true}},
	{"*deepseek-chat*", Capabilities{Tools: true}},
	{"*deepseek*", Capabilities{Reasoning: true, Tools: true}},

	{"*minimax*image*", Capabilities{ImageOutput: true, Tools: true}},
	{"*minimax-m3*", Capabilities{Vision: true, Reasoning: true, Tools: true}},
	{"*minimax-m2.7*", Capabilities{Reasoning: true, Tools: true}},
	{"*minimax*", Capabilities{Reasoning: true, Tools: true}},

	{"*mimo*v2.5*", Capabilities{Vision: true, AudioInput: true, VideoInput: true, Tools: true}},
	{"*mimo*omni*", Capabilities{Vision: true, AudioInput: true, Tools: true}},
	{"*mimo*", Capabilities{Vision: true, Tools: true}},

	{"*llama-4*", Capabilities{Vision: true, Tools: true}},
	{"*llama*", Capabilities{Tools: true}},

	{"*codestral*", Capabilities{Tools: true}},
	{"*mistral-large*", Capabilities{Vision: true, Tools: true}},
	{"*mistral*", Capabilities{Tools: true}},

	// Wally (RunAnywhere) serves three models behind one host, and only
	// deepseek-v4.1-flash takes image input. The provider is deliberately not
	// in chat.visionProviders, which is all-or-nothing per provider; the
	// per-model split lives here instead.
	{"*deepseek-v4.1*", Capabilities{Vision: true, Tools: true}},

	{"*command-a-vision*", Capabilities{Vision: true, Tools: true}},
	{"*command*", Capabilities{Tools: true}},

	{"*sonar*", Capabilities{Search: true, Tools: true}},
	{"*pplx*", Capabilities{Search: true, Tools: true}},
	{"*perplexity*", Capabilities{Search: true, Tools: true}},

	{"*laguna-s-2.1*free*", Capabilities{Reasoning: true, Tools: true}},
	{"*laguna-s-2.1*", Capabilities{Reasoning: true, Tools: true}},
	{"*laguna*", Capabilities{Reasoning: true, Tools: true}},

	{"*hunyuan*", Capabilities{Reasoning: true, Tools: true}},
	{"hy3*", Capabilities{Reasoning: true, Tools: true}},
	{"*hy4*", Capabilities{Reasoning: true, Tools: true}},
	{"*longcat*", Capabilities{Tools: true}},
	{"*step-*", Capabilities{Reasoning: true, Tools: true}},
	{"*nemotron*", Capabilities{Reasoning: true, Tools: true}},
	{"*ling-*", Capabilities{Reasoning: true, Tools: true}},
	{"*muse-spark*", Capabilities{Vision: true, Reasoning: true, Tools: true}},
}

// matchPattern checks if a string matches a glob pattern (only supports * as wildcard)
func matchPattern(pattern, s string) bool {
	matched, err := path.Match(strings.ToLower(pattern), strings.ToLower(s))
	if err != nil {
		return false
	}
	return matched
}

// GetModelTokenLimits returns the context window and maximum output tokens for a model.
//
// It resolves from the model name alone, so prefer GetModelTokenLimitsFor when
// the provider is known: the synced catalog is keyed by provider/model and
// carries the authoritative upstream figures.
func GetModelTokenLimits(model string) (contextWindow int, maxOutput int) {
	return getModelTokenLimitsByPattern(model)
}

// GetModelTokenLimitsFor resolves token limits for a provider/model pair.
//
// Resolution order:
//  1. The models.dev catalog synced into the local database, which is keyed by
//     provider and so is the only source that distinguishes two providers
//     serving the same model name.
//  2. Name-pattern matching, for models the catalog does not list.
//
// The catalog is consulted first because pattern matching is a guess: it maps
// any unrecognised name to a single default, so a small model could otherwise
// advertise a 128K window it cannot serve.
func GetModelTokenLimitsFor(provider, model string) (contextWindow int, maxOutput int) {
	if cw, mo := GetCatalogLimits(provider, model); cw > 0 || mo > 0 {
		return cw, mo
	}
	return getModelTokenLimitsByPattern(model)
}

// GetDisplayTokenLimits is GetModelTokenLimitsFor for the dashboard's model
// list, where a number the operator reads as fact is worse than a blank.
//
// It differs in one place: when the synced catalog has nothing AND the name
// matches no known model family, it returns 0 instead of the pattern matcher's
// default of 128K. That default is a placeholder for routing, where any cap
// beats none — but rendered as "128K context" beside a custom or brand-new
// model it is an invented limit, and the operator has no way to tell it apart
// from a measured one. A match on a real family (gemini-3, claude-opus, …) is
// genuine information and is returned as-is: providers like antigravity are
// absent from models.dev, so their models would otherwise carry no label at
// all despite the family window being well known.
func GetDisplayTokenLimits(provider, model string) (contextWindow int, maxOutput int) {
	if cw, mo := GetCatalogLimits(provider, model); cw > 0 || mo > 0 {
		return cw, mo
	}
	if _, ok := matchTokenFamily(model); !ok {
		return 0, 0
	}
	return getModelTokenLimitsByPattern(model)
}

// getModelTokenLimitsByPattern guesses token limits from the model name.
//
// This is a fallback for models absent from the synced catalog. It matches
// substrings, so ordering matters and the default branch is a guess rather than
// a measurement — see GetModelTokenLimitsFor.
// tokenFamily is one row of the name-pattern table: a set of substrings
// identifying a model family, and the limits that family serves.
type tokenFamily struct {
	substr []string
	ctx    int
	out    int
}

// tokenFamilies is the single source of truth for name-pattern matching. Order
// matters: the first row whose substring matches wins, so the most specific
// names must come first. getModelTokenLimitsByPattern and matchesKnownFamily
// both read it, which is what keeps the routing fallback and the dashboard
// label from drifting apart.
var tokenFamilies = []tokenFamily{
	{[]string{"deepseek-v4.1-flash", "deepseek-v4-flash"}, 1000000, 128000},
	{[]string{"gemini-1.5", "gemini-2.0", "gemini-2.5", "gemini-3", "glm-5.3-flash"}, 1048576, 65536},
	{[]string{"grok-4.5", "grok-4.6"}, 524288, 32768},
	{[]string{"gpt-6"}, 272000, 128000},
	{[]string{"claude-3", "claude-sonnet", "claude-opus", "claude-haiku"}, 200000, 8192},
	{[]string{"gpt-4o", "gpt-4-turbo", "gpt-4.1", "gpt-5"}, 128000, 16384},
	{[]string{"solar-pro", "longcat"}, 200000, 32000},
	{[]string{"o1", "o3"}, 200000, 100000},
	{[]string{"deepseek", "qwen", "glm", "kimi"}, 131072, 8192},
}

// matchTokenFamily returns the family a model name belongs to, or false when no
// family matches. The false case is what getModelTokenLimitsByPattern turns
// into its 128K default, and what GetDisplayTokenLimits refuses to label.
func matchTokenFamily(model string) (tokenFamily, bool) {
	m := strings.ToLower(model)
	for _, f := range tokenFamilies {
		for _, s := range f.substr {
			if strings.Contains(m, s) {
				return f, true
			}
		}
	}
	return tokenFamily{}, false
}

func getModelTokenLimitsByPattern(model string) (contextWindow int, maxOutput int) {
	if f, ok := matchTokenFamily(model); ok {
		return f.ctx, f.out
	}
	return 128000, 4096
}

// GetCapabilitiesForModel resolves capabilities using the fallback chain.
func GetCapabilitiesForModel(provider, model string) Capabilities {
	if model == "" {
		return DefaultCapabilities
	}

	key := unique.Make(provider + "||" + model)
	capsCacheMu.RLock()
	if cached, ok := capsCache[key]; ok {
		capsCacheMu.RUnlock()
		return cached
	}
	capsCacheMu.RUnlock()

	baseModel := model
	if i := strings.LastIndex(model, "/"); i >= 0 {
		baseModel = model[i+1:]
	}

	// 1. Provider-specific override
	var res Capabilities
	resolved := false

	if provider != "" {
		if pCaps, ok := providerCapabilities[provider]; ok {
			if caps, ok := pCaps[model]; ok {
				res = mergeCapabilities(DefaultCapabilities, caps)
				resolved = true
			} else if caps, ok := pCaps[baseModel]; ok {
				res = mergeCapabilities(DefaultCapabilities, caps)
				resolved = true
			}
		}
	}

	// 2. Canonical exact
	if !resolved {
		if caps, ok := modelCapabilities[baseModel]; ok {
			res = mergeCapabilities(DefaultCapabilities, caps)
			resolved = true
		} else if caps, ok := modelCapabilities[model]; ok {
			res = mergeCapabilities(DefaultCapabilities, caps)
			resolved = true
		}
	}

	// 3. Pattern match
	if !resolved {
		for _, p := range patternCapabilities {
			if matchPattern(p.pattern, baseModel) || matchPattern(p.pattern, model) {
				res = mergeCapabilities(DefaultCapabilities, p.caps)
				resolved = true
				break
			}
		}
	}
	if !resolved {
		res = DefaultCapabilities
	}

	// 5. Synced models.dev overlay. It only ever turns capabilities ON: the
	// hand-written tables above still win where models.dev lags (kimi video),
	// and switching Tools off on a catalogue miss would make clients stop
	// sending tools to a model that handles them.
	if dynamic := GetCatalogModalities(provider, model); dynamic != nil {
		res.Vision = res.Vision || dynamic.Vision
		res.PDF = res.PDF || dynamic.PDF
		res.AudioInput = res.AudioInput || dynamic.AudioInput
		res.VideoInput = res.VideoInput || dynamic.VideoInput
		res.ImageOutput = res.ImageOutput || dynamic.ImageOutput
		res.Tools = res.Tools || dynamic.Tools
		res.Reasoning = res.Reasoning || dynamic.Reasoning
	}

	// 6. Custom model caps (from kv customModels) — additive, like dynamic
	if custom, ok := GetCustomModelCaps(provider, model); ok {
		if custom.Vision {
			res.Vision = true
		}
		if custom.Reasoning {
			res.Reasoning = true
		}
		if custom.Search {
			res.Search = true
		}
		if custom.PDF {
			res.PDF = true
		}
		if custom.AudioInput {
			res.AudioInput = true
		}
		if custom.VideoInput {
			res.VideoInput = true
		}
		if custom.ImageOutput {
			res.ImageOutput = true
		}
		if custom.AudioOutput {
			res.AudioOutput = true
		}
		if custom.Tools {
			res.Tools = true
		}
	} else if custom, ok := GetCustomModelCaps(provider, baseModel); ok {
		if custom.Vision {
			res.Vision = true
		}
		if custom.Reasoning {
			res.Reasoning = true
		}
		if custom.Search {
			res.Search = true
		}
		if custom.PDF {
			res.PDF = true
		}
		if custom.AudioInput {
			res.AudioInput = true
		}
		if custom.VideoInput {
			res.VideoInput = true
		}
		if custom.ImageOutput {
			res.ImageOutput = true
		}
		if custom.AudioOutput {
			res.AudioOutput = true
		}
		if custom.Tools {
			res.Tools = true
		}
	}

	capsCacheMu.Lock()
	capsCache[key] = res
	capsCacheMu.Unlock()

	return res
}

func mergeCapabilities(base, overlay Capabilities) Capabilities {
	if overlay.Vision {
		base.Vision = true
	}
	if overlay.PDF {
		base.PDF = true
	}
	if overlay.AudioInput {
		base.AudioInput = true
	}
	if overlay.VideoInput {
		base.VideoInput = true
	}
	if overlay.ImageOutput {
		base.ImageOutput = true
	}
	if overlay.AudioOutput {
		base.AudioOutput = true
	}
	if overlay.Search {
		base.Search = true
	}
	if !overlay.Tools {
		// if specifically disabled (Tools is true by default usually, but we check if we need to turn it off)
		// Wait, the merge logic in JS is { ...DEFAULT, ...caps }.
		// So if overlay sets tools: false, it should be false.
		// In Go, bool zero value is false. So we can't tell if overlay didn't set it, or set it to false.
		// However, in our hardcoded maps above, I explicitly included Tools: true for all that have it,
		// and we can assume any overlay boolean that is `false` is meant to be false if it differs from default.
		// Actually, to make it simple, let's just use the overlay if it has truthy values, except Tools which we default to true.
		// Let's just do a naive merge.
	}
	// For Go, since we define complete Capabilities structs in the maps with Tools: true where needed:
	return Capabilities{
		Vision:      base.Vision || overlay.Vision,
		PDF:         base.PDF || overlay.PDF,
		AudioInput:  base.AudioInput || overlay.AudioInput,
		VideoInput:  base.VideoInput || overlay.VideoInput,
		ImageOutput: base.ImageOutput || overlay.ImageOutput,
		AudioOutput: base.AudioOutput || overlay.AudioOutput,
		Search:      base.Search || overlay.Search,
		Tools:       overlay.Tools, // We made sure to set Tools:true in all overlays where it applies. If it's omitted, it becomes false. Wait, DefaultCapabilities has Tools=true. Let's make sure our maps above have Tools:true for everything except gpt-image-1.
		Reasoning:   base.Reasoning || overlay.Reasoning,
	}
}

// CapabilitiesDetail matches the serializable capabilities object expected by clients in /v1/models.
type CapabilitiesDetail struct {
	Vision                  bool `json:"vision"`
	PDF                     bool `json:"pdf"`
	AudioInput              bool `json:"audioInput"`
	VideoInput              bool `json:"videoInput"`
	ImageOutput             bool `json:"imageOutput"`
	AudioOutput             bool `json:"audioOutput"`
	ThinkingCanDisable      bool `json:"thinkingCanDisable"`
	ThinkingRange           any  `json:"thinkingRange"`
	ThinkingEffortSupported bool `json:"thinkingEffortSupported"`
	ContextWindows          int  `json:"contextWindows,omitempty"`
	ContextWindow           int  `json:"contextWindow,omitempty"`
}

// GetCapabilitiesDetailForModel returns the full JSON-serializable capabilities map for /v1/models.
func GetCapabilitiesDetailForModel(provider, model string) CapabilitiesDetail {
	caps := GetCapabilitiesForModel(provider, model)
	cw, _ := GetModelTokenLimitsFor(provider, model)
	if cw == 0 {
		cw, _ = GetModelTokenLimits(provider + "/" + model)
	}
	if cw == 0 {
		cw = 128000
	}
	return CapabilitiesDetail{
		Vision:                  caps.Vision,
		PDF:                     caps.PDF,
		AudioInput:              caps.AudioInput,
		VideoInput:              caps.VideoInput,
		ImageOutput:             caps.ImageOutput,
		AudioOutput:             caps.AudioOutput,
		ThinkingCanDisable:      true,
		ThinkingRange:           nil,
		ThinkingEffortSupported: false,
		ContextWindows:          cw,
		ContextWindow:           cw,
	}
}
