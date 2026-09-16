package providers

import (
	"strings"
)

// Category mirrors the exact category keys defined in the upstream Next.js
// 9router provider registry (`src/app/.../providers.js`, webpack chunk 98236):
//
//	free      -> g("free")      (100% free, no key)
//	freeTier  -> g("freeTier")  (free tier / free credits)
//	oauth     -> g("oauth")     (OAuth / CLI session)
//	apikey    -> g("apikey")    (paid API key)
//	webCookie -> g("webCookie") (web session cookie)
type Category string

const (
	CategoryFree      Category = "free"      // 100% Free / no auth
	CategoryFreeTier  Category = "freeTier"  // Free Tier & Free Credits
	CategoryOAuth     Category = "oauth"     // OAuth & CLI Sessions
	CategoryAPIKey    Category = "apikey"    // API Key Providers
	CategoryWebCookie Category = "webCookie" // Web Session Cookie
	CategoryCustom    Category = "custom"    // openai-compatible-* / anthropic-compatible-*
)

// Category order used by the Next.js dashboard sections (top -> bottom):
// Compatible Endpoints, OAuth, Free Tier (free + freeTier), API Key.
var CategoryOrder = []Category{
	CategoryOAuth,
	CategoryFreeTier,
	CategoryFree,
	CategoryAPIKey,
	CategoryWebCookie,
	CategoryCustom,
}

// Upstream category aliases: Next.js groups "free" under the "Free Tier" section.
var categorySection = map[Category]Category{
	CategoryFree: CategoryFreeTier,
}

// ProviderMeta stores classification and display metadata extracted from the
// upstream Next.js registry.
type ProviderMeta struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Alias       string   `json:"alias,omitempty"`
	Category    Category `json:"category"`
	AuthType    string   `json:"authType"` // "oauth", "apikey", "cookie", or "none"
	Description string   `json:"description,omitempty"`

	// Display metadata mirrored from upstream `display:{...}` so the embedded
	// dashboard renders the same icon, colour and notices as the Next.js UI.
	Priority        int      `json:"priority,omitempty"`
	Icon            string   `json:"icon,omitempty"`
	Color           string   `json:"color,omitempty"`
	Website         string   `json:"website,omitempty"`
	SignupURL       string   `json:"signupUrl,omitempty"`
	Deprecated      bool     `json:"deprecated,omitempty"`
	DeprecationNote string   `json:"deprecationNotice,omitempty"`
	ServiceKinds    []string `json:"serviceKinds,omitempty"`
}

// RiskNoticeText is shown for providers whose upstream registry marks them
// deprecated with a RISK_NOTICE placeholder. Verbatim from upstream bundle.
const RiskNoticeText = "⚠️ Risk Notice: This provider uses a subscription/OAuth session not officially licensed for proxy/router use. Account may be restricted or banned. Use at your own risk."

// serviceKindMeta maps upstream service kind IDs to their display label and
// Material Symbols icon, transcribed from the Next.js client bundle.
type serviceKindMeta struct {
	Label string `json:"label"`
	Icon  string `json:"icon"`
}

var serviceKindLabels = map[string]serviceKindMeta{
	"llm":         {Label: "LLM", Icon: "chat"},
	"embedding":   {Label: "Embedding", Icon: "data_array"},
	"image":       {Label: "Text to Image", Icon: "brush"},
	"imageToText": {Label: "Image to Text", Icon: "image_search"},
	"tts":         {Label: "Text To Speech", Icon: "record_voice_over"},
	"stt":         {Label: "Speech To Text", Icon: "mic"},
	"webSearch":   {Label: "Web Search", Icon: "travel_explore"},
	"webFetch":    {Label: "Web Fetch", Icon: "language"},
	"video":       {Label: "Video", Icon: "movie"},
	"music":       {Label: "Music", Icon: "music_note"},
}

// GetServiceKindLabels exposes the service-kind vocabulary for the UI.
func GetServiceKindLabels() map[string]serviceKindMeta {
	return serviceKindLabels
}

// providerRegistry maps canonical provider IDs to their upstream metadata.
// Categories are transcribed verbatim from the Next.js 9router source build
// (webpack module 98236) so the Go dashboard matches the Node.js behaviour.
var providerRegistry = map[string]ProviderMeta{
	// ---- category: "oauth" (OAuth & CLI Sessions) ----
	"antigravity":    {ID: "antigravity", Name: "Antigravity", Alias: "ag", Category: CategoryOAuth, AuthType: "oauth", Description: "Google Antigravity (Gemini) Cloud Code session", Priority: 20, Icon: "rocket_launch", Color: "#F59E0B", ServiceKinds: []string{"llm", "image", "webSearch"}, Deprecated: true, DeprecationNote: RiskNoticeText, Website: "https://antigravity.google"},
	"claude":         {ID: "claude", Name: "Claude Code", Alias: "cc", Category: CategoryOAuth, AuthType: "oauth", Description: "Anthropic Claude Code CLI authorization token", Priority: 10, Icon: "smart_toy", Color: "#D97757", ServiceKinds: []string{"llm"}, Deprecated: true, DeprecationNote: RiskNoticeText, Website: "https://claude.ai"},
	"cline":          {ID: "cline", Name: "Cline", Alias: "cl", Category: CategoryOAuth, AuthType: "oauth", Description: "Cline autonomous coding agent integration", Priority: 80, Icon: "smart_toy", Color: "#5B9BD5", ServiceKinds: []string{"llm"}, Website: "https://cline.bot"},
	"clinepass":      {ID: "clinepass", Name: "ClinePass", Alias: "clinepass", Category: CategoryOAuth, AuthType: "oauth", Description: "Cline pass proxy relay", Priority: 85, Icon: "vpn_key", Color: "#5B9BD5", ServiceKinds: []string{"llm"}, Website: "https://cline.bot"},
	"codebuddy-cn":   {ID: "codebuddy-cn", Name: "CodeBuddy CN", Alias: "cbcn", Category: CategoryOAuth, AuthType: "oauth", Description: "CodeBuddy CN developer session"},
	"codebuddy-intl": {ID: "codebuddy-intl", Name: "CodeBuddy", Alias: "cbai", Category: CategoryOAuth, AuthType: "oauth", Description: "CodeBuddy developer session"},
	"codex":          {ID: "codex", Name: "OpenAI Codex", Alias: "cx", Category: CategoryOAuth, AuthType: "oauth", Description: "OpenAI Codex CLI session tokens", Priority: 30, Icon: "code", Color: "#3B82F6", ServiceKinds: []string{"llm", "image"}, Deprecated: true, DeprecationNote: RiskNoticeText, Website: "https://chatgpt.com/codex"},
	"cursor":         {ID: "cursor", Name: "Cursor IDE", Alias: "cu", Category: CategoryOAuth, AuthType: "oauth", Description: "Cursor editor token / session import", Priority: 50, Icon: "edit_note", Color: "#00D4AA", ServiceKinds: []string{"llm"}, Website: "https://cursor.com"},
	"github":         {ID: "github", Name: "GitHub Copilot", Alias: "gh", Category: CategoryOAuth, AuthType: "oauth", Description: "GitHub Copilot Chat & Completion session", Priority: 40, Icon: "code", Color: "#333333", ServiceKinds: []string{"llm", "embedding"}, Deprecated: true, DeprecationNote: RiskNoticeText, Website: "https://github.com/features/copilot"},
	"gitlab":         {ID: "gitlab", Name: "GitLab Duo", Alias: "gitlab", Category: CategoryOAuth, AuthType: "oauth", Description: "GitLab Duo AI developer token"},
	"grok-cli":       {ID: "grok-cli", Name: "Grok CLI (Grok Build)", Alias: "gcli", Category: CategoryOAuth, AuthType: "oauth", Description: "xAI Grok CLI developer session", Priority: 275, Icon: "auto_awesome", Color: "#1DA1F2", ServiceKinds: []string{"llm"}, Website: "https://x.ai"},
	"iflow":          {ID: "iflow", Name: "iFlow AI", Alias: "if", Category: CategoryOAuth, AuthType: "oauth", Description: "iFlow multi-model platform session"},
	"kilocode":       {ID: "kilocode", Name: "Kilo Code", Alias: "kc", Category: CategoryOAuth, AuthType: "oauth", Description: "Kilo Code IDE session", Priority: 70, Icon: "code", Color: "#FF6B35", ServiceKinds: []string{"llm"}, Website: "https://kilocode.ai"},
	"kimi":           {ID: "kimi", Name: "Kimi", Alias: "kimi", Category: CategoryOAuth, AuthType: "oauth", Description: "Moonshot Kimi coding session", Priority: 170, Icon: "psychology", Color: "#1E3A8A", ServiceKinds: []string{"llm", "webSearch"}, Website: "https://kimi.moonshot.cn"},
	"qoder":          {ID: "qoder", Name: "Qoder", Alias: "qd", Category: CategoryOAuth, AuthType: "oauth", Description: "Alibaba Qoder IDE session", Priority: 30, Icon: "water_drop", Color: "#EC4899", ServiceKinds: []string{"llm"}, Website: "https://qoder.com"},
	"xai":            {ID: "xai", Name: "xAI (Grok)", Alias: "xai", Category: CategoryOAuth, AuthType: "oauth", Description: "xAI Grok OAuth session", Priority: 280, Icon: "auto_awesome", Color: "#1DA1F2", ServiceKinds: []string{"llm", "imageToText", "webSearch", "image", "video"}, Website: "https://x.ai"},
	"xiaomi-mimo":    {ID: "xiaomi-mimo", Name: "Xiaomi MiMo", Alias: "xiaomi-mimo", Category: CategoryOAuth, AuthType: "oauth", Description: "Xiaomi MiMo coding session", Priority: 290, Icon: "smart_toy", Color: "#FF6900", ServiceKinds: []string{"llm", "tts"}, Website: "https://xiaomimimo.com"},
	"zed":            {ID: "zed", Name: "Zed", Alias: "zd", Category: CategoryOAuth, AuthType: "oauth", Description: "Zed editor LLM session", Priority: 10, Icon: "code", Color: "#A855F7", ServiceKinds: []string{"llm"}, Website: "https://zed.dev"},

	// ---- category: "freeTier" (Free Tier & Free Credits) ----
	"byteplus":      {ID: "byteplus", Name: "BytePlus ModelArk", Alias: "byteplus", Category: CategoryFreeTier, AuthType: "apikey", Description: "BytePlus ModelArk free credits for new accounts", Priority: 70, Icon: "cloud", Color: "#2563EB", ServiceKinds: []string{"llm"}, Website: "https://console.byteplus.com/ark"},
	"cloudflare-ai": {ID: "cloudflare-ai", Name: "Cloudflare", Alias: "cloudflare-ai", Category: CategoryFreeTier, AuthType: "apikey", Description: "Workers AI free tier (API token + Account ID)", Priority: 60, Icon: "cloud", Color: "#F38020", ServiceKinds: []string{"llm", "image"}, Website: "https://developers.cloudflare.com/workers-ai/"},
	"coqui":         {ID: "coqui", Name: "Coqui TTS", Alias: "coqui", Category: CategoryFreeTier, AuthType: "none", Description: "Local Coqui TTS server"},
	"edge-tts":      {ID: "edge-tts", Name: "Edge TTS", Alias: "edge-tts", Category: CategoryFreeTier, AuthType: "none", Description: "Microsoft Edge TTS (no auth)"},
	"gemini":        {ID: "gemini", Name: "Gemini", Alias: "gemini", Category: CategoryFreeTier, AuthType: "apikey", Description: "Google AI Studio Gemini free tier", Priority: 50, Icon: "diamond", Color: "#4285F4", ServiceKinds: []string{"llm", "embedding", "image", "imageToText", "webSearch", "tts", "stt"}, Website: "https://ai.google.dev"},
	"google-tts":    {ID: "google-tts", Name: "Google TTS", Alias: "google-tts", Category: CategoryFreeTier, AuthType: "none", Description: "Google TTS (no auth)"},
	"kimchi":        {ID: "kimchi", Name: "Kimchi", Alias: "kimchi", Category: CategoryFreeTier, AuthType: "apikey", Description: "Kimchi free tier (OAuth or API key)", Priority: 95, Icon: "restaurant", Color: "#FF521D", ServiceKinds: []string{"llm", "imageToText"}, Website: "https://kimchi.dev"},
	"local-device":  {ID: "local-device", Name: "Local Device", Alias: "local-device", Category: CategoryFreeTier, AuthType: "none", Description: "Local device TTS"},
	"nvidia":        {ID: "nvidia", Name: "NVIDIA NIM", Alias: "nvidia", Category: CategoryFreeTier, AuthType: "apikey", Description: "NVIDIA Developer Program free access", Priority: 20, Icon: "developer_board", Color: "#76B900", ServiceKinds: []string{"llm", "tts", "embedding"}, Website: "https://developer.nvidia.com/nim"},
	"ollama":        {ID: "ollama", Name: "Ollama Cloud", Alias: "ollama", Category: CategoryFreeTier, AuthType: "apikey", Description: "Ollama Cloud free tier", Priority: 30, Icon: "cloud", Color: "#ffffffff", ServiceKinds: []string{"llm", "webFetch"}, Website: "https://ollama.com"},
	"openrouter":    {ID: "openrouter", Name: "OpenRouter", Alias: "openrouter", Category: CategoryFreeTier, AuthType: "apikey", Description: "OpenRouter free models tier", Priority: 10, Icon: "router", Color: "#F97316", ServiceKinds: []string{"llm", "embedding", "tts", "imageToText", "video"}, Website: "https://openrouter.ai"},
	"searxng":       {ID: "searxng", Name: "SearXNG", Alias: "searxng", Category: CategoryFreeTier, AuthType: "none", Description: "Self-hosted SearXNG search (no auth)"},
	"tortoise":      {ID: "tortoise", Name: "Tortoise TTS", Alias: "tortoise", Category: CategoryFreeTier, AuthType: "none", Description: "Local Tortoise TTS server"},
	"vertex":        {ID: "vertex", Name: "Vertex AI", Alias: "vertex", Category: CategoryFreeTier, AuthType: "apikey", Description: "Google Cloud Vertex AI ($300 free credits)", Priority: 40, Icon: "cloud", Color: "#4285F4", ServiceKinds: []string{"llm", "imageToText", "video"}, Website: "https://cloud.google.com/vertex-ai"},
	"api-airforce":  {ID: "api-airforce", Name: "API.airforce", Alias: "af", Category: CategoryFreeTier, AuthType: "apikey", Description: "API.airforce free gateway"},
	"bazaarlink":    {ID: "bazaarlink", Name: "Bazaarlink", Alias: "bzl", Category: CategoryFreeTier, AuthType: "apikey", Description: "Bazaarlink free gateway"},
	"kilo-gateway":  {ID: "kilo-gateway", Name: "Kilo Gateway", Alias: "kgw", Category: CategoryFreeTier, AuthType: "apikey", Description: "Kilo Gateway free tier"},
	"poolside":      {ID: "poolside", Name: "Poolside", Alias: "ps", Category: CategoryFreeTier, AuthType: "apikey", Description: "Poolside free tier", Priority: 60, Icon: "water_drop", Color: "#0EA5E9", ServiceKinds: []string{"llm", "embedding", "image"}, Website: "https://poolside.ai"},
	"opencode-go":   {ID: "opencode-go", Name: "OpenCode Go", Alias: "opencode-go", Category: CategoryFreeTier, AuthType: "apikey", Description: "OpenCode Go gateway", Priority: 210, Icon: "terminal", Color: "#E87040", ServiceKinds: []string{"llm"}, Website: "https://opencode.ai/auth"},

	// ---- category: "free" (100% Free / no auth) ----
	"gemini-cli": {ID: "gemini-cli", Name: "Gemini CLI", Alias: "gc", Category: CategoryFree, AuthType: "oauth", Description: "Google Gemini CLI free OAuth session", Priority: 20, Icon: "terminal", Color: "#4285F4", ServiceKinds: []string{"llm"}, Deprecated: true, DeprecationNote: RiskNoticeText, Website: "https://github.com/google-gemini/gemini-cli"},
	"kiro":       {ID: "kiro", Name: "Kiro AI", Alias: "kr", Category: CategoryFree, AuthType: "oauth", Description: "Kiro AI free developer session", Priority: 10, Icon: "psychology_alt", Color: "#FF6B35", ServiceKinds: []string{"webSearch"}, Deprecated: true, DeprecationNote: RiskNoticeText, Website: "https://kiro.dev"},
	"mimo-free":  {ID: "mimo-free", Name: "MiMo Code Free", Alias: "mmf", Category: CategoryFree, AuthType: "oauth", Description: "Xiaomi MiMo Code free session"},
	"opencode":   {ID: "opencode", Name: "OpenCode Free", Alias: "oc", Category: CategoryFree, AuthType: "none", Description: "Free public coding assistance endpoints", Priority: 40, Icon: "terminal", Color: "#E87040", ServiceKinds: []string{"llm"}},

	// ---- category: "apikey" (API Key Providers) ----
	"alicode-intl":         {ID: "alicode-intl", Name: "Alibaba Coding", Alias: "alicode-intl", Category: CategoryAPIKey, AuthType: "apikey", Priority: 10, Icon: "cloud", Color: "#FF6A00", ServiceKinds: []string{"llm"}, Website: "https://www.alibabacloud.com/product/coding"},
	"alicode":              {ID: "alicode", Name: "Alibaba", Alias: "alicode", Category: CategoryAPIKey, AuthType: "apikey", Priority: 20, Icon: "cloud", Color: "#FF6A00", ServiceKinds: []string{"llm"}, Website: "https://bailian.console.aliyun.com"},
	"anthropic":            {ID: "anthropic", Name: "Anthropic", Alias: "anthropic", Category: CategoryAPIKey, AuthType: "apikey", Priority: 30, Icon: "smart_toy", Color: "#D97757", ServiceKinds: []string{"llm", "imageToText"}, Website: "https://console.anthropic.com"},
	"assemblyai":           {ID: "assemblyai", Name: "AssemblyAI", Alias: "assemblyai", Category: CategoryAPIKey, AuthType: "apikey", Priority: 30, Icon: "record_voice_over", Color: "#0062FF", ServiceKinds: []string{"stt"}, Website: "https://assemblyai.com"},
	"aws-polly":            {ID: "aws-polly", Name: "AWS Polly", Alias: "polly", Category: CategoryAPIKey, AuthType: "apikey"},
	"azure":                {ID: "azure", Name: "Azure OpenAI", Alias: "azure", Category: CategoryAPIKey, AuthType: "apikey", Priority: 40, Icon: "cloud", Color: "#0078D4", ServiceKinds: []string{"llm"}, Website: "https://azure.microsoft.com/en-us/products/ai-services/openai-service"},
	"black-forest-labs":    {ID: "black-forest-labs", Name: "Black Forest Labs", Alias: "black-forest-labs", Category: CategoryAPIKey, AuthType: "apikey", Priority: 50, Icon: "image", Color: "#111827", ServiceKinds: []string{"image"}, Website: "https://blackforestlabs.ai"},
	"blackbox":             {ID: "blackbox", Name: "Blackbox AI", Alias: "blackbox", Category: CategoryAPIKey, AuthType: "apikey", Priority: 50, Icon: "smart_toy", Color: "#5B5FEF", ServiceKinds: []string{"llm"}, Website: "https://blackbox.ai"},
	"brave-search":         {ID: "brave-search", Name: "Brave Search", Alias: "brave", Category: CategoryAPIKey, AuthType: "apikey"},
	"cartesia":             {ID: "cartesia", Name: "Cartesia", Alias: "cartesia", Category: CategoryAPIKey, AuthType: "apikey"},
	"cerebras":             {ID: "cerebras", Name: "Cerebras", Alias: "cerebras", Category: CategoryAPIKey, AuthType: "apikey", Priority: 60, Icon: "memory", Color: "#FF4F00", ServiceKinds: []string{"llm"}, Website: "https://www.cerebras.ai"},
	"chutes":               {ID: "chutes", Name: "Chutes AI", Alias: "chutes", Category: CategoryAPIKey, AuthType: "apikey", Priority: 70, Icon: "water_drop", Color: "#ffffffff", ServiceKinds: []string{"llm"}, Website: "https://chutes.ai"},
	"cohere":               {ID: "cohere", Name: "Cohere", Alias: "cohere", Category: CategoryAPIKey, AuthType: "apikey", Priority: 90, Icon: "hub", Color: "#39594D", ServiceKinds: []string{"llm"}, Website: "https://cohere.com"},
	"comfyui":              {ID: "comfyui", Name: "ComfyUI", Alias: "comfyui", Category: CategoryAPIKey, AuthType: "apikey", Priority: 120, Icon: "account_tree", Color: "#4CAF50", ServiceKinds: []string{"image"}, Website: "https://github.com/comfyanonymous/ComfyUI"},
	"commandcode":          {ID: "commandcode", Name: "Command Code", Alias: "commandcode", Category: CategoryAPIKey, AuthType: "apikey", Priority: 100, Icon: "smart_toy", Color: "#000000", ServiceKinds: []string{"tts"}, Website: "https://commandcode.ai"},
	"deepgram":             {ID: "deepgram", Name: "Deepgram", Alias: "deepgram", Category: CategoryAPIKey, AuthType: "apikey", Priority: 20, Icon: "mic", Color: "#13EF93", ServiceKinds: []string{"stt"}, Website: "https://deepgram.com"},
	"deepseek":             {ID: "deepseek", Name: "DeepSeek", Alias: "deepseek", Category: CategoryAPIKey, AuthType: "apikey", Priority: 110, Icon: "bolt", Color: "#4D6BFE", ServiceKinds: []string{"llm"}, Website: "https://deepseek.com"},
	"elevenlabs":           {ID: "elevenlabs", Name: "ElevenLabs", Alias: "el", Category: CategoryAPIKey, AuthType: "apikey"},
	"exa":                  {ID: "exa", Name: "Exa", Alias: "exa", Category: CategoryAPIKey, AuthType: "apikey"},
	"fal-ai":               {ID: "fal-ai", Name: "Fal.ai", Alias: "fal-ai", Category: CategoryAPIKey, AuthType: "apikey", Priority: 90, Icon: "image", Color: "#2563EB", ServiceKinds: []string{"image"}, Website: "https://fal.ai"},
	"featherless":          {ID: "featherless", Name: "Featherless", Alias: "featherless", Category: CategoryAPIKey, AuthType: "apikey", Priority: 65, Icon: "flutter_dash", Color: "#111827", ServiceKinds: []string{"webFetch"}, Website: "https://featherless.ai"},
	"firecrawl":            {ID: "firecrawl", Name: "Firecrawl", Alias: "firecrawl", Category: CategoryAPIKey, AuthType: "apikey"},
	"fireworks":            {ID: "fireworks", Name: "Fireworks AI", Alias: "fireworks", Category: CategoryAPIKey, AuthType: "apikey", Priority: 50, Icon: "local_fire_department", Color: "#7B2EF2", ServiceKinds: []string{"llm", "embedding"}, Website: "https://fireworks.ai"},
	"glm-cn":               {ID: "glm-cn", Name: "GLM (China)", Alias: "glm-cn", Category: CategoryAPIKey, AuthType: "apikey", Priority: 130, Icon: "code", Color: "#DC2626", ServiceKinds: []string{"webSearch"}, Website: "https://open.bigmodel.cn"},
	"glm":                  {ID: "glm", Name: "GLM Coding", Alias: "glm", Category: CategoryAPIKey, AuthType: "apikey", Priority: 140, Icon: "code", Color: "#2563EB", ServiceKinds: []string{"llm", "webSearch"}, Website: "https://open.bigmodel.cn"},
	"google-pse":           {ID: "google-pse", Name: "Google PSE", Alias: "gpse", Category: CategoryAPIKey, AuthType: "apikey"},
	"groq":                 {ID: "groq", Name: "Groq", Alias: "groq", Category: CategoryAPIKey, AuthType: "apikey", Priority: 60, Icon: "speed", Color: "#F55036", ServiceKinds: []string{"llm", "imageToText", "stt"}, Website: "https://groq.com"},
	"huggingface":          {ID: "huggingface", Name: "HuggingFace", Alias: "huggingface", Category: CategoryAPIKey, AuthType: "apikey", Priority: 70, Icon: "face", Color: "#FFD21E", ServiceKinds: []string{"image", "stt"}, Website: "https://huggingface.co"},
	"hyperbolic":           {ID: "hyperbolic", Name: "Hyperbolic", Alias: "hyperbolic", Category: CategoryAPIKey, AuthType: "apikey", Priority: 160, Icon: "bolt", Color: "#00D4FF", ServiceKinds: []string{"tts"}, Website: "https://hyperbolic.xyz"},
	"inworld":              {ID: "inworld", Name: "Inworld TTS", Alias: "inworld", Category: CategoryAPIKey, AuthType: "apikey"},
	"jina-ai":              {ID: "jina-ai", Name: "Jina AI", Alias: "jina", Category: CategoryAPIKey, AuthType: "apikey"},
	"jina-reader":          {ID: "jina-reader", Name: "Jina Reader", Alias: "jina-reader", Category: CategoryAPIKey, AuthType: "apikey"},
	"linkup":               {ID: "linkup", Name: "Linkup", Alias: "linkup", Category: CategoryAPIKey, AuthType: "apikey"},
	"minimax-cn":           {ID: "minimax-cn", Name: "Minimax (China)", Alias: "minimax-cn", Category: CategoryAPIKey, AuthType: "apikey", Priority: 190, Icon: "memory", Color: "#DC2626", ServiceKinds: []string{"llm", "tts"}, Website: "https://www.minimaxi.com"},
	"minimax":              {ID: "minimax", Name: "Minimax Coding", Alias: "minimax", Category: CategoryAPIKey, AuthType: "apikey", Priority: 90, Icon: "memory", Color: "#7C3AED", ServiceKinds: []string{"llm", "image", "imageToText", "webSearch", "tts"}, Website: "https://www.minimaxi.com"},
	"mistral":              {ID: "mistral", Name: "Mistral", Alias: "mistral", Category: CategoryAPIKey, AuthType: "apikey", Priority: 80, Icon: "air", Color: "#FF7000", ServiceKinds: []string{"llm", "imageToText", "embedding"}, Website: "https://mistral.ai"},
	"mmf":                  {ID: "mmf", Name: "MMF", Alias: "mmf", Category: CategoryAPIKey, AuthType: "apikey"},
	"nanobanana":           {ID: "nanobanana", Name: "NanoBanana API", Alias: "nanobanana", Category: CategoryAPIKey, AuthType: "apikey", Priority: 80, Icon: "extension", Color: "#FFD700", ServiceKinds: []string{"image"}, Website: "https://nanobananaapi.ai"},
	"nebius":               {ID: "nebius", Name: "Nebius AI", Alias: "nebius", Category: CategoryAPIKey, AuthType: "apikey", Priority: 70, Icon: "cloud", Color: "#6C5CE7", ServiceKinds: []string{"llm", "embedding"}, Website: "https://nebius.com"},
	"ollama-local":         {ID: "ollama-local", Name: "Ollama Local", Alias: "ollama-local", Category: CategoryAPIKey, AuthType: "apikey", Priority: 50, Icon: "cloud", Color: "#ffffffff", ServiceKinds: []string{"llm"}, Website: "https://ollama.com"},
	"ollama-search":        {ID: "ollama-search", Name: "Ollama Search", Alias: "ollama-search", Category: CategoryAPIKey, AuthType: "apikey"},
	"openai":               {ID: "openai", Name: "OpenAI", Alias: "openai", Category: CategoryAPIKey, AuthType: "apikey", Priority: 30, Icon: "auto_awesome", Color: "#10A37F", ServiceKinds: []string{"llm", "embedding", "tts", "stt", "image", "imageToText", "webSearch"}, Website: "https://platform.openai.com"},
	"perplexity":           {ID: "perplexity", Name: "Perplexity", Alias: "perplexity", Category: CategoryAPIKey, AuthType: "apikey", Priority: 180, Icon: "search", Color: "#20808D", ServiceKinds: []string{"llm", "webSearch"}, Website: "https://www.perplexity.ai"},
	"perplexity-agent":     {ID: "perplexity-agent", Name: "Perplexity Agent", Alias: "perplexity-agent", Category: CategoryAPIKey, AuthType: "apikey", Priority: 181, Icon: "travel_explore", Color: "#20808D", ServiceKinds: []string{"llm", "webSearch"}, Website: "https://www.perplexity.ai"},
	"playht":               {ID: "playht", Name: "PlayHT", Alias: "playht", Category: CategoryAPIKey, AuthType: "apikey"},
	"recraft":              {ID: "recraft", Name: "Recraft", Alias: "recraft", Category: CategoryAPIKey, AuthType: "apikey", Priority: 70, Icon: "image", Color: "#EC4899", ServiceKinds: []string{"image"}, Website: "https://recraft.ai"},
	"runwayml":             {ID: "runwayml", Name: "Runway ML", Alias: "runwayml", Category: CategoryAPIKey, AuthType: "apikey", Priority: 80, Icon: "movie", Color: "#000000", ServiceKinds: []string{"image"}, Website: "https://runwayml.com"},
	"sdwebui":              {ID: "sdwebui", Name: "SD WebUI", Alias: "sdwebui", Category: CategoryAPIKey, AuthType: "apikey", Priority: 110, Icon: "brush", Color: "#FF7043", ServiceKinds: []string{"image"}, Website: "https://github.com/AUTOMATIC1111/stable-diffusion-webui"},
	"searchapi":            {ID: "searchapi", Name: "SearchAPI", Alias: "searchapi", Category: CategoryAPIKey, AuthType: "apikey"},
	"serper":               {ID: "serper", Name: "Serper", Alias: "serper", Category: CategoryAPIKey, AuthType: "apikey"},
	"siliconflow":          {ID: "siliconflow", Name: "SiliconFlow", Alias: "siliconflow", Category: CategoryAPIKey, AuthType: "apikey", Priority: 250, Icon: "cloud_queue", Color: "#5B6EF5", ServiceKinds: []string{"llm"}, Website: "https://cloud.siliconflow.com"},
	"stability-ai":         {ID: "stability-ai", Name: "Stability AI", Alias: "stability-ai", Category: CategoryAPIKey, AuthType: "apikey", Priority: 60, Icon: "image", Color: "#8B5CF6", ServiceKinds: []string{"image"}, Website: "https://stability.ai"},
	"tavily":               {ID: "tavily", Name: "Tavily", Alias: "tavily", Category: CategoryAPIKey, AuthType: "apikey"},
	"together":             {ID: "together", Name: "Together AI", Alias: "together", Category: CategoryAPIKey, AuthType: "apikey", Priority: 60, Icon: "group_work", Color: "#0F6FFF", ServiceKinds: []string{"llm", "embedding"}, Website: "https://www.together.ai"},
	"topaz":                {ID: "topaz", Name: "Topaz", Alias: "topaz", Category: CategoryAPIKey, AuthType: "apikey"},
	"venice":               {ID: "venice", Name: "Venice AI", Alias: "venice", Category: CategoryAPIKey, AuthType: "apikey", Priority: 115, Icon: "shield", Color: "#DC2626", ServiceKinds: []string{"llm", "embedding", "image"}, Website: "https://venice.ai"},
	"vercel-ai-gateway":    {ID: "vercel-ai-gateway", Name: "Vercel AI Gateway", Alias: "vercel-ai-gateway", Category: CategoryAPIKey, AuthType: "apikey", Priority: 160, Icon: "deployed_code", Color: "#111827", ServiceKinds: []string{"llm", "embedding", "image", "imageToText", "webSearch"}, Website: "https://vercel.com/ai-gateway"},
	"vertex-partner":       {ID: "vertex-partner", Name: "Vertex Partner", Alias: "vertex-partner", Category: CategoryAPIKey, AuthType: "apikey", Priority: 260, Icon: "cloud", Color: "#34A853", ServiceKinds: []string{"llm"}, Website: "https://cloud.google.com/vertex-ai/generative-ai/docs/partner-models/use-partner-models"},
	"volcengine-ark":       {ID: "volcengine-ark", Name: "Volcengine Ark", Alias: "volcengine-ark", Category: CategoryAPIKey, AuthType: "apikey", Priority: 270, Icon: "cloud", Color: "#1677FF", ServiceKinds: []string{"llm"}, Website: "https://ark.cn-beijing.volces.com"},
	"voyage-ai":            {ID: "voyage-ai", Name: "Voyage AI", Alias: "voyage-ai", Category: CategoryAPIKey, AuthType: "apikey", Priority: 40, Icon: "data_array", Color: "#0EA5E9", ServiceKinds: []string{"embedding"}, Website: "https://www.voyageai.com"},
	"xiaomi-tokenplan":     {ID: "xiaomi-tokenplan", Name: "Xiaomi MiMo (Token Plan)", Alias: "xiaomi-tokenplan", Category: CategoryAPIKey, AuthType: "apikey", Priority: 300, Icon: "smart_toy", Color: "#FF6700", ServiceKinds: []string{"webSearch"}, Website: "https://mimo.xiaomi.com"},
	"youcom":               {ID: "youcom", Name: "You.com Search", Alias: "youcom", Category: CategoryAPIKey, AuthType: "apikey"},
	"alims-intl":           {ID: "alims-intl", Name: "Alibaba Studio", Alias: "alims-intl", Category: CategoryAPIKey, AuthType: "apikey", Priority: 11, Icon: "cloud", Color: "#FF6A00", ServiceKinds: []string{"llm"}, Website: "https://modelstudio.console.alibabacloud.com"},
	"baidu":                {ID: "baidu", Name: "Baidu Qianfan", Alias: "qianfan", Category: CategoryAPIKey, AuthType: "apikey"},
	"bluesminds":           {ID: "bluesminds", Name: "BluesMinds", Alias: "bm", Category: CategoryAPIKey, AuthType: "apikey"},
	"llm7":                 {ID: "llm7", Name: "LLM7", Alias: "llm7", Category: CategoryAPIKey, AuthType: "apikey"},
	"sambanova":            {ID: "sambanova", Name: "SambaNova", Alias: "samba", Category: CategoryAPIKey, AuthType: "apikey"},
	"tencent":              {ID: "tencent", Name: "Tencent Hunyuan", Alias: "hunyuan", Category: CategoryAPIKey, AuthType: "apikey"},
	"morph":                {ID: "morph", Name: "Morph", Alias: "morph", Category: CategoryAPIKey, AuthType: "apikey"},
	"tokenrouter":          {ID: "tokenrouter", Name: "TokenRouter", Alias: "tokenrouter", Category: CategoryAPIKey, AuthType: "apikey"},
	"selfhosted-stt":       {ID: "selfhosted-stt", Name: "Self-hosted STT", Alias: "selfhosted-stt", Category: CategoryAPIKey, AuthType: "apikey", Priority: 50, Icon: "cloud", Color: "#ffffffff", ServiceKinds: []string{"stt"}, Website: "https://github.com/ggml-org/whisper.cpp"},
	"selfhosted-tts":       {ID: "selfhosted-tts", Name: "Self-hosted TTS", Alias: "selfhosted-tts", Category: CategoryAPIKey, AuthType: "apikey", Priority: 50, Icon: "cloud", Color: "#ffffffff", ServiceKinds: []string{"tts"}, Website: "https://github.com/remsky/Kokoro-FastAPI"},
	"selfhosted-embedding": {ID: "selfhosted-embedding", Name: "Self-hosted Embedding", Alias: "selfhosted-embedding", Category: CategoryAPIKey, AuthType: "apikey", Priority: 50, Icon: "cloud", Color: "#ffffffff", ServiceKinds: []string{"embedding"}, Website: "https://github.com/ggml-org/llama.cpp"},
	"fish-audio":           {ID: "fish-audio", Name: "Fish Audio", Alias: "fish", Category: CategoryAPIKey, AuthType: "apikey"},
	"alitp-intl":           {ID: "alitp-intl", Name: "Alibaba Token Plan", Alias: "alitp-intl", Category: CategoryAPIKey, AuthType: "apikey", Priority: 11, Icon: "cloud", Color: "#FF6A00", ServiceKinds: []string{"webSearch"}, Website: "https://www.alibabacloud.com/campaign/ai-landing-page-token"},
	"xquik":                {ID: "xquik", Name: "Xquik", Alias: "xquik", Category: CategoryAPIKey, AuthType: "apikey"},

	// ---- category: "webCookie" (Web Session Cookie) ----
	"grok-web":       {ID: "grok-web", Name: "Grok Web (Subscription)", Alias: "gw", Category: CategoryWebCookie, AuthType: "cookie", Description: "Paste your sso= cookie from grok.com", Priority: 150, Icon: "auto_awesome", Color: "#1DA1F2", ServiceKinds: []string{"llm"}, Website: "https://grok.com"},
	"perplexity-web": {ID: "perplexity-web", Name: "Perplexity Web (Pro/Max)", Alias: "pw", Category: CategoryWebCookie, AuthType: "cookie", Description: "Paste your __Secure-next-auth.session-token from perplexity.ai", Priority: 220, Icon: "search", Color: "#20808D", ServiceKinds: []string{"llm"}, Website: "https://www.perplexity.ai"},
}

// Custom (compatible) endpoint prefixes, matching upstream constants.
const (
	CustomOpenAIPrefix    = "openai-compatible-"
	CustomAnthropicPrefix = "anthropic-compatible-"
	CustomEmbeddingPrefix = "custom-embedding-"
)

// IsCustomCompatible reports whether a provider ID denotes a user-defined
// OpenAI/Anthropic-compatible endpoint (rendered as "Compatible Endpoints").
func IsCustomCompatible(providerID string) bool {
	s := strings.ToLower(strings.TrimSpace(providerID))
	return strings.HasPrefix(s, CustomOpenAIPrefix) ||
		strings.HasPrefix(s, CustomAnthropicPrefix) ||
		strings.HasPrefix(s, CustomEmbeddingPrefix)
}

// ClassifyProvider determines the upstream category, its display label and the
// human-facing display name for any provider string.
func ClassifyProvider(providerID string) (category Category, categoryLabel string, displayName string) {
	raw := strings.TrimSpace(providerID)
	norm := strings.ToLower(raw)

	// Resolve short aliases to canonical IDs.
	if canonical, ok := ProviderAliasMap[norm]; ok {
		norm = canonical
	}

	// Custom / compatible endpoints come first (matches Next.js UI section 1).
	if IsCustomCompatible(norm) {
		return CategoryCustom, GetCategoryLabel(CategoryCustom), raw
	}

	// Exact registry match (upstream-defined categories).
	if meta, exists := providerRegistry[norm]; exists {
		return meta.Category, GetCategoryLabel(meta.Category), meta.Name
	}

	// Known-but-unregistered compatible-ish self-hosted names.
	if strings.HasPrefix(norm, "ollama") ||
		strings.HasPrefix(norm, "custom") ||
		strings.HasPrefix(norm, "vllm") ||
		strings.HasPrefix(norm, "litellm") ||
		strings.HasPrefix(norm, "local") {
		return CategoryCustom, GetCategoryLabel(CategoryCustom), raw
	}

	// Unknown provider: default to the API Key section, preserving the raw ID.
	if raw == "" {
		return CategoryAPIKey, GetCategoryLabel(CategoryAPIKey), "Unknown"
	}
	return CategoryAPIKey, GetCategoryLabel(CategoryAPIKey), raw
}

// GetCategoryLabel returns the upstream section title for a category.
func GetCategoryLabel(cat Category) string {
	switch cat {
	case CategoryOAuth:
		return "OAuth Providers"
	case CategoryFreeTier:
		return "Free Tier Providers"
	case CategoryFree:
		return "Free Tier Providers"
	case CategoryAPIKey:
		return "API Key Providers"
	case CategoryWebCookie:
		return "Web Cookie Providers"
	case CategoryCustom:
		return "Compatible Endpoints"
	default:
		return "Other Providers"
	}
}

// SectionCategory normalises "free" into the "Free Tier" section used by the UI.
func SectionCategory(cat Category) Category {
	if section, ok := categorySection[cat]; ok {
		return section
	}
	return cat
}

// GetCatalogByCategory groups known providers by category for UI menus/options,
// mirroring the Next.js `Object.entries(g(category))` catalog shape.
func GetCatalogByCategory() map[Category][]ProviderMeta {
	catalog := map[Category][]ProviderMeta{
		CategoryOAuth:     {},
		CategoryFreeTier:  {},
		CategoryFree:      {},
		CategoryAPIKey:    {},
		CategoryWebCookie: {},
		CategoryCustom: {
			{
				ID:          "openai-compatible",
				Name:        "OpenAI Compatible",
				Alias:       "openai-compatible",
				Category:    CategoryCustom,
				AuthType:    "apikey",
				Description: "Any OpenAI-compatible endpoint (Ollama, vLLM, LiteLLM, LM Studio)",
			},
			{
				ID:          "anthropic-compatible",
				Name:        "Anthropic Compatible",
				Alias:       "anthropic-compatible",
				Category:    CategoryCustom,
				AuthType:    "apikey",
				Description: "Any Anthropic-compatible endpoint",
			},
		},
	}

	for _, meta := range providerRegistry {
		catalog[meta.Category] = append(catalog[meta.Category], meta)
	}

	return catalog
}

// GetProviderMeta looks up registry metadata for a provider ID, tolerating the
// custom-endpoint prefixes (openai-compatible-*, anthropic-compatible-*).
func GetProviderMeta(id string) (ProviderMeta, bool) {
	if m, ok := providerRegistry[id]; ok {
		return m, true
	}
	if IsCustomCompatible(id) {
		return ProviderMeta{
			ID:          id,
			Name:        id,
			Category:    CategoryCustom,
			AuthType:    "apikey",
			Description: "Custom compatible endpoint",
			Icon:        "dns",
			Color:       "#2EA043",
			Priority:    90,
		}, true
	}
	return ProviderMeta{}, false
}
