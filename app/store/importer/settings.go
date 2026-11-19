package importer

import "strings"

type settingClass int

const (
	classUnknown settingClass = iota
	classCopy
	classSecurity
	classSkip
)

// copiedSettings are routing, provider behavior, token saver, auto plan and
// skill activation keys: configuration a new deployment should start with.
var copiedSettings = map[string]bool{
	"stickyRoundRobinLimit": true, "providerStrategies": true, "quotaVisibility": true, "comboStrategy": true,
	"comboStickyRoundRobinLimit": true, "comboStrategies": true, "capacityAdapter": true, "fallbackStrategy": true,
	"providerThinking": true, "providerOverrides": true, "claudeAutoPing": true, "codexAutoPing": true, "char_limit": true,
	"rtkEnabled": true, "headroomEnabled": true, "headroomUrl": true, "headroomCompressUserMessages": true, "headroomTimeoutMs": true,
	"cavemanEnabled": true, "cavemanLevel": true, "ponytailEnabled": true, "ponytailLevel": true,
	"cavemanRoutingMode": true, "ponytailRoutingMode": true,
	"customPromptEnabled": true, "customPrompt": true, "tokenSaverEnabled": true, "tokenSaverBudget": true,
	"autoPlanEnabled": true, "autoPlanMode": true, "autoPlanComboId": true, "autoCodeComboId": true,
	"autoPlanComplexityThreshold": true, "autoPlanSmartClassify": true, "autoPlanMaxTokens": true,
	"autoPlanTimeoutMs": true, "autoPlanShowInResponse": true, "ccFilterNaming": true, "responseCache": true,
}

// securitySettings are the login policy. They move only with --with-sso.
var securitySettings = map[string]bool{
	"authMode": true, "ssoType": true, "oidcIssuerUrl": true, "oidcClientId": true, "oidcClientSecret": true,
	"oidcScopes": true, "oidcLoginLabel": true, "samlEntryPoint": true, "samlIssuer": true, "samlCert": true,
	"samlLoginLabel": true, "samlIdpEntityId": true, "samlAttributeEmail": true, "samlAttributeName": true,
}

// skippedSettings belong to the old deployment or to features this program does
// not run: credentials, proxies, tunnels, observability, process managers.
var skippedSettings = map[string]bool{
	"password": true, "mitmSudoEncrypted": true, "requireLogin": true, "requireApiKey": true, "tunnelDashboardAccess": true,
	"cloudEnabled": true, "cloudUrl": true, "tunnelEnabled": true, "tunnelUrl": true, "tunnelProvider": true,
	"tailscaleEnabled": true, "tailscaleUrl": true, "dnsToolEnabled": true, "mitmRouterBaseUrl": true,
	"outboundProxyEnabled": true, "outboundProxyUrl": true, "outboundNoProxy": true,
	"enableObservability": true, "observabilityMaxRecords": true, "observabilityBatchSize": true,
	"observabilityFlushIntervalMs": true, "observabilityMaxJsonSize": true,
	"headroomAutoStart": true, "hermesServiceAutoStart": true,
	"pxpipeEnabled": true, "pxpipeAutoInstall": true, "pxpipeMinChars": true, "pxpipeTimeoutMs": true,
}

// classifySetting says what an import does with a settings key. A key it does
// not know is reported by name, never guessed.
func classifySetting(key string) settingClass {
	switch {
	case copiedSettings[key]:
		return classCopy
	case securitySettings[key]:
		return classSecurity
	case skippedSettings[key]:
		return classSkip
	case strings.HasSuffix(key, "Enabled"), strings.HasSuffix(key, "RoutingMode"), strings.HasPrefix(key, "ecc_auto_skill_router"):
		return classCopy
	}
	return classUnknown
}
