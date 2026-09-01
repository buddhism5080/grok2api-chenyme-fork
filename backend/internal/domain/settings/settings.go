package settings

import "time"

const (
	DefaultBuildResponseHeaderTimeout = 5 * time.Minute
	MinBuildResponseHeaderTimeout     = 30 * time.Second
	MaxBuildResponseHeaderTimeout     = 30 * time.Minute

	DefaultBuildStreamIdleTimeout = 2 * time.Minute
	MinBuildStreamIdleTimeout     = 30 * time.Second
	MaxBuildStreamIdleTimeout     = 10 * time.Minute

	// Stream first-character (TTFT) timeout for Build streaming requests.
	// Measured from response headers received until the first body byte/token.
	// Disabled by default; when enabled the timeout must be within [Min, Max].
	DefaultBuildStreamFirstCharTimeoutEnabled = false
	DefaultBuildStreamFirstCharTimeout        = 15 * time.Second
	MinBuildStreamFirstCharTimeout            = 5 * time.Second
	MaxBuildStreamFirstCharTimeout            = 60 * time.Second

	DefaultWebStreamIdleTimeout     = 90 * time.Second
	DefaultConsoleStreamIdleTimeout = 2 * time.Minute
	MinProviderStreamIdleTimeout    = 30 * time.Second
	MaxProviderStreamIdleTimeout    = 10 * time.Minute

	DefaultWebFreeVideoDurationCap = 6
	MinWebFreeVideoDurationCap     = 1
	MaxWebFreeVideoDurationCap     = 15
)

func NormalizeWebFreeVideoDurationCap(value int) int {
	if value < MinWebFreeVideoDurationCap || value > MaxWebFreeVideoDurationCap {
		return DefaultWebFreeVideoDurationCap
	}
	return value
}

// Config 表示可跨重启持久化并支持热加载的网关运行参数。
type Config struct {
	Server            ServerConfig
	ProviderBuild     ProviderBuildConfig
	ProviderWeb       ProviderWebConfig
	ProviderConsole   ProviderConsoleConfig
	Batch             BatchConfig
	Media             MediaConfig
	Frontend          FrontendConfig
	Routing           RoutingConfig
	Audit             AuditConfig
	ClientKeyDefaults ClientKeyDefaultsConfig
	Accounts          AccountsConfig
}

// ServerConfig 定义可热更新的推理入口容量参数。
type ServerConfig struct {
	MaxConcurrentRequests int
}

// FrontendConfig 定义公开 API 地址的运行时覆盖值；留空时使用配置文件值。
type FrontendConfig struct {
	PublicAPIBaseURL string
}

type ProviderConsoleConfig struct {
	BaseURL           string
	ChatTimeout       time.Duration
	StreamIdleTimeout time.Duration
}

type MediaConfig struct {
	MaxImageBytes           int64
	MaxTotalBytes           int64
	CleanupThresholdPercent int
	CleanupInterval         time.Duration
}

type ProviderWebConfig struct {
	BaseURL              string
	StatsigMode          string
	StatsigManualValue   string
	StatsigSignerURL     string
	ClearanceMode        string
	FlareSolverrURL      string
	ClearanceTimeout     time.Duration
	ClearanceRefresh     time.Duration
	QuotaTimeout         time.Duration
	ChatTimeout          time.Duration
	StreamIdleTimeout    time.Duration
	ImageTimeout         time.Duration
	VideoTimeout         time.Duration
	MediaConcurrency     int
	AllowNSFW            bool
	FreeVideoDurationCap int
	RecoveryBackoffBase  time.Duration
	RecoveryBackoffMax   time.Duration
}

// BatchConfig 定义账号导入、转换、同步和凭据刷新的并发上限。
type BatchConfig struct {
	ImportConcurrency     int
	ConversionConcurrency int
	SyncConcurrency       int
	RefreshConcurrency    int
	RandomDelay           *time.Duration
}

// ProviderBuildConfig 定义 Grok Build CLI 上游协议标识。
type ProviderBuildConfig struct {
	BaseURL               string
	FallbackBaseURL       string
	ClientVersion         string
	ClientIdentifier      string
	TokenAuth             string
	UserAgent             string
	ResponseHeaderTimeout time.Duration
	StreamIdleTimeout     time.Duration
	// StreamFirstCharTimeoutEnabled gates the first-char timeout; default false.
	StreamFirstCharTimeoutEnabled bool
	// StreamFirstCharTimeout is the timeout from response headers to first body byte/token.
	// Only enforced when StreamFirstCharTimeoutEnabled is true.
	StreamFirstCharTimeout time.Duration
}

// RoutingConfig 定义会话粘性、冷却和故障切换边界。
type RoutingConfig struct {
	StickyTTL        time.Duration
	CooldownBase     time.Duration
	CooldownMax      time.Duration
	CapacityWait     time.Duration
	MaxAttempts      int
	VideoMaxAttempts int
	PreferFreeBuild  bool
	// MarkBuildChatDeniedAsReauth 为 true 时，Build chat 权限拒绝标 reauthRequired，默认 false 保留模型级冷却。
	MarkBuildChatDeniedAsReauth bool
	// AccountIsolatedConnections is optional so persisted payloads written by
	// older releases do not silently override a value supplied by config.yaml.
	AccountIsolatedConnections *bool
	// BuildHighTokenSpeedAutoDisable 为 true 时，Build 渠道指定模型输出速度超阈值则自动禁用账号；默认关闭。
	BuildHighTokenSpeedAutoDisable *bool
	// BuildHighTokenSpeedThreshold 输出 Token/s 阈值；开启时默认 1000。
	BuildHighTokenSpeedThreshold *float64
	// BuildHighTokenSpeedModelIDs 需要监控的公开模型 ID（如 grok-4.20）；空列表表示不生效。
	BuildHighTokenSpeedModelIDs []string
	// BuildHighTokenSpeedOverheadMS 固定从总耗时扣除的延迟预算（毫秒），默认 2000。
	// 自动禁用速度：speed = (outputTokens + reasoningTokens) * 1000 / (durationMS - overheadMS)
	// 与审计页不同：审计页用 durationMS - firstTokenMS，且通常只计 outputTokens。
	BuildHighTokenSpeedOverheadMS *int64
	// BuildUsagePenaltyTokenThreshold 是 Build Free 账号的 input+output token 调度惩罚阈值。
	// 0 表示关闭。达到阈值后该账号 24 小时内尽量不被选中。
	BuildUsagePenaltyTokenThreshold int64
	SegmentedSelector               *SegmentedSelectorConfig
}

type SegmentedSelectorConfig struct {
	ActiveEnabled bool
	MinCandidates int
	WindowSize    int
}

type AuditConfig struct {
	BufferSize    int
	BatchSize     int
	FlushInterval time.Duration
	CommitDelay   time.Duration
	RetentionDays *int
}

// ClientKeyDefaultsConfig 定义新建客户端密钥的默认限制。
type ClientKeyDefaultsConfig struct {
	RPMLimit      int
	MaxConcurrent int
}

// AccountsConfig 定义账号池后台维护策略；默认全部关闭。
type AccountsConfig struct {
	// MarkBuildForbiddenReauth marks high-confidence Grok Build permission denials as requiring reauthorization.
	MarkBuildForbiddenReauth bool
	// BuildForbiddenReauthCodes contains exact upstream error codes that opt into account invalidation.
	BuildForbiddenReauthCodes []string
	// ExcludeBuildBotFlaggedFromScheduling 为 true 时，bot_flag_source/bfs∈{1,2} 的 Build 账号不参与调度。
	// 仅影响 ProviderBuild 选号；关联 Web/Console 账号调度不受影响。
	ExcludeBuildBotFlaggedFromScheduling bool
	// AutoCleanReauthEnabled 为 true 时，周期性删除已标记 reauthRequired 且超过 minAge 的账号。
	AutoCleanReauthEnabled bool
	// AutoCleanReauthInterval 自动清理扫描间隔。
	AutoCleanReauthInterval time.Duration
	// AutoCleanReauthMinAge 仅删除 reauth_marked_at 早于该时长的 reauthRequired 账号。
	AutoCleanReauthMinAge time.Duration
	// AutoCleanIncludeDisabled 为 true 时，reauth 清理时包含 enabled=false 的账号。
	AutoCleanIncludeDisabled bool
}
