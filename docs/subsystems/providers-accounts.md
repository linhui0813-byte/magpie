# Providers and accounts

The gateway can send a request to three kinds of upstream:

- A **provider** in `providers.json`: an endpoint with one or more API keys.
- A **subscription account**: an agent's own sign-in (Codex, Claude Code, Copilot and others) that magpie reads from the agent's store, or an account signed in from magpie.
- A **plugin's account**: a subscription served by an OpenCode plugin.

Package `internal/provider` holds all three, the routing groups built over
them, and what is left of each one's allowance.

Plugin ownership of the deprecated built-in subscriptions is in
[Provider and plugin ownership](provider-plugins.md). How the gateway orders
keys and accounts per request is in [Gateway routing and fallback](gateway-routing.md).

## Responsibilities and sources of truth

| Part | Responsibility | Source |
| --- | --- | --- |
| Provider | `Provider` holds the endpoint bases for each `Protocol` (Chat, Responses, Anthropic, Gemini), the keys, `Fallback`, `Routing`, `Sink`, `Affinity`, concurrency and queue limits, `Headers`, `Proxy`, models, and balance settings. `BaseAPI` is the API a custom provider's editor shows its Base URL as: the one the user picked and saved, while it has a URL, so a provider with a chat URL beside its Responses one isn't shown as OpenAI compatible after Responses was picked. Requests go by which URLs are set. `Account` is set only for a subscription and is never saved (`json:"-"`). | [`provider.go`](../../internal/provider/provider.go) |
| providers.json | `Path` is `providers.json` in magpie's config folder. A read that fails returns `ErrUnreadable` and is never treated as empty. `store` writes atomically through a rename and drops the cached catalog (`catalog.Touched`). | [`provider.go`](../../internal/provider/provider.go) |
| The list | `All` merges stored providers with `Accounts()`. An entry with no URL stores the user's picks for an account. The order is `file.Order`. Reads within one request share one build (`Hold`, #746). | [`provider.go`](../../internal/provider/provider.go) |
| Keys | Several keys per provider. Requests go to the first key that is on; the gateway moves on when it is out or rate limited. `Key` stays the first, so other code reads it as before. A key can be made for one protocol (`KeyProtocol`). | [`keys.go`](../../internal/provider/keys.go) |
| Subscription accounts | `Accounts` reads the credentials each agent keeps on disk or in the macOS Keychain; nothing is stored twice. A built-in that has moved to its plugin is dropped (`Moved`), and plugin accounts are placed in its slot (`placeMoved`). `Sign` adds the account's own auth, then the user's `Headers`. | [`account.go`](../../internal/provider/account.go) |
| Token refresh | Each account refreshes through its vendor. For example, `codexToken` refreshes within 5 minutes of expiry and writes the rotated tokens back into `~/.codex/auth.json` for Codex CLI. A 400 or 401 from the vendor's refresh endpoint means it was refused (`refreshFailed`). | [`account.go`](../../internal/provider/account.go) |
| Saved accounts | `logins.json` remembers every Codex and Claude Code account seen signed in. A switch moves the credentials into the agent's own store and saves the ones it replaces. A saved account that is on (`SetLoginOn`) stands behind the agent's own. Its tokens stay in `logins.json`. | [`logins.go`](../../internal/provider/logins.go), [`logins_on.go`](../../internal/provider/logins_on.go) |
| Sign-in in magpie | `StartSignIn` runs the vendor's own browser OAuth with its client and takes the tokens at a callback on this machine. A browser that can't reach that callback (magpie on a server or in Docker) ends on a page that won't load; its address, pasted (`SubmitSignInCallback`), goes through the same callback handler (`pastedCallback`). A plugin's browser sign-in (`auto`) whose page names a port on this machine to come back to takes its pasted address too: magpie opens it there on 127.0.0.1, for the plugin's own listener (`pluginCallback`, `loopbackPorts`); Command Code's page posts its key from script, so it offers the API key way instead. A Claude account is signed in by Claude Code itself. Signing in brings back an account removed from magpie (`ShowAccount`). A sign-in started in magpie (`StartSignIn`, `StartPluginSignIn`) reads as done (`SignInStatus`, `WaitSignIn`) only after `finish` has run `ShowAccount` for its account, so the window, which opens the account as soon as it sees done, finds it listed. If providers.json can't be written then, the account stays hidden and the sign-in still ends done. Signing in to a plugin's provider from the command line (`pluginLogin`, with a key or the browser) brings it back too. It also takes off the mark a sign-in its vendor turned away left on the account (`Lapsed`) and shows again the agent's own account removed in magpie (`clearPluginLapse`). The window's plugin sign-in does the same with the browser (`pluginDone`, then `finish`) or with a key (`PluginAPIKey`); a key and the command line share these steps in `PluginSignedIn`. | [`signin.go`](../../internal/provider/signin.go), [`claude_signin.go`](../../internal/provider/claude_signin.go), [`pluginsignin.go`](../../internal/provider/pluginsignin.go), [`pluginsignin_paste.go`](../../internal/provider/pluginsignin_paste.go), [`plugins_cli.go`](../../plugins_cli.go) |
| Used-up switch | When the account Codex or Claude Code is signed in to has used its allowance, the agent is signed in to the next account that is on and has room (#208). | [`codex_switch.go`](../../internal/provider/codex_switch.go) |
| Routing groups | User-made groups from the Routing view, and auto-found groups of the same model across providers (derived, never stored). Members are `provider/model[:effort]`. A group can nest another (`group/<id>`); `SaveGroup` refuses loops. `Match` takes a glob or `re:` pattern (#766). | [`group.go`](../../internal/provider/group.go) |
| Allowance | `Quotas` asks every vendor at once: subscription windows (`SubscriptionUsage`), key-bought plans (`PlanQuotas`) and key balances (`KeyBalances`). The Usage page, `magpie quota --json` and `GET /v1/magpie/quotas` read it. A single card can be read again on its own (#840). A remote magpie's cards come from what it has kept (`CachedCards`, `GET /v1/magpie/quotas/cards`) and are never read there for this computer except by a card's refresh (`POST /v1/magpie/quotas/refresh`, throttled per card). | [`quotas.go`](../../internal/provider/quotas.go), [`subscription_usage.go`](../../internal/provider/subscription_usage.go), [`planquota.go`](../../internal/provider/planquota.go), [`balance.go`](../../internal/provider/balance.go), [`quota_refresh.go`](../../internal/provider/quota_refresh.go), [`remote_quotas.go`](../../internal/provider/remote_quotas.go) |
| A key's own windows | A sub2api key given a spending limit per 5 hours, day or 7 days answers its `/v1/usage` with `rate_limits` and no `remaining` (`readSub2APIKeyLimits`). With the Balance URL set to it, the key's card shows them as the windows *5 hours*, *1 day*, *7 days*, in USD, beside a balance field the user wrote; *Check balance* and `magpie provider show` give what its fullest window has left (`Balance`). `KeyAllowance` gives routing the same windows: never waited for, read in the background at most once a minute, a failed read keeping the last, and seeded from the card's last reading (`quotas.json`) after a restart.  A key bought as a plan (Kimi Code, GLM Coding Plan and Z.ai, MiniMax Coding Plan, OpenCode Go, Command Code: `planQuotaSourceOf`) gives routing its card's windows the same way (`planKeyWindows`, Zhipu's team quota behind a key with none of its own); the Usage page's read of its card is taken at once (`noteKeyAllowance`), and a key there that turns out pay-as-you-go, with no windows, is asked again only every 30 minutes (`keyNoWindowsAge`). StepFun's Step Plan is read through a sign-in, not the key, and isn't one of them. | [`sub2api_usage.go`](../../internal/provider/sub2api_usage.go), [`key_allowance.go`](../../internal/provider/key_allowance.go) |
| Last served | `served.json` records when each account, plan or key last answered, shown as `lastServedAt` (#570). | [`served.go`](../../internal/provider/served.go) |
| Protocol detection | Works out which APIs a base URL speaks when a provider is added. | [`detect.go`](../../internal/provider/detect.go) |

## Runtime path

1. **Add.** The Providers page adds a key provider through `Add` or `Save`, or an account through `StartSignIn`. An agent signed in on its own is found by `Accounts` with no step from the user.
2. **Resolve.** The gateway calls `provider.Resolve` or `GroupFor` with the model name, then plans candidates over the provider's keys or accounts.
3. **Sign.** For each try, `Sign` gives the request its auth. A token about to expire is refreshed first, and the rotated tokens go back to their one holder: the agent's store, or `logins.json` for an account standing behind it.
4. **Allowance.** Usage endpoints are rate limited, so `SubscriptionUsage` caches its results: a cached copy comes back at once and a stale one is refreshed in the background. When an account's windows start again, `OnRenewed` tells the gateway so a resting account can come back. A key's reading that finds a window it was full in (at `SpentShareOf` its routing) full no more, or full till sooner, tells it too, as agent `""` and the key's `KeyAllowanceID`, so a key out of its limit is back once the limit is raised or its usage reset; a first reading, a failed one, or one that finds it fuller tells nothing.

## Constraints and failure behavior

Volcengine Ark's account balance is read by
[`volcengine_balance.go`](../../internal/provider/volcengine_balance.go),
through the official billing
[`QueryBalanceAcct` API](https://www.volcengine.com/docs/6269/1223898). The model API key
cannot authenticate that query. In the provider editor's **Account balance**
field, enter `Access Key ID:Secret Access Key` for the same Volcengine account,
with permission to read the account balance through `QueryBalanceAcct`. This pair is stored in
`Provider.BalanceToken`; the providers API returns only whether it is set
and `balanceToken.kind = "volcengine"`, never the credentials. It is used
only to sign the billing lookup, without the model key or provider headers.

The Usage page shows `AvailableBalance` in the returned currency (CNY by
default), once per enabled Ark provider, shared across its model keys. It
includes available credit, so it is not necessarily cash. Coding Plan and
Agent Plan endpoints show this same account balance, not plan allowance.
Ark endpoints are recognized by host, including imports; a custom Balance
URL still overrides the built-in reader. Missing billing credentials leave
the card visible with setup instructions. Reads share the balance cache,
history and per-card refresh. Temporary failures can show a dated last
reading; changing the billing credentials cannot reuse another account's
reading. Billing refusals and invalid or missing balances remain errors,
never zero. Neither balance lookup nor its failure affects model routing.

Qwen Token Plan's personal allowance is read by
[`qwenquota.go`](../../internal/provider/qwenquota.go), through the same
Bailian console gateway as the [official CLI](https://github.com/modelstudioai/cli/blob/main/packages/commands/src/commands/usage/token-plan.ts).
Its model API key cannot authenticate the console lookup. Sign in to the
account that owns the plan with `bl auth login --console` (install the CLI
with `npm install -g bailian-cli`), then refresh the card. Magpie reads the
CLI's `~/.bailian/config.json`, or `BAILIAN_CONFIG_DIR/config.json`, following
`active_config` for a named profile. Magpie never copies that sign-in or
writes the CLI's configuration itself.
The profile must use the domestic Beijing console. A missing or expired
session leaves the Qwen card visible with a sign-in instruction.

When the console refuses an expired session, and that same CLI profile has
a static Alibaba Cloud AccessKey ID and Secret (`access_key_id` and
`access_key_secret`), [`qwenquota_renew.go`](../../internal/provider/qwenquota_renew.go)
asks the installed `bl` (or `bailian`) CLI to read this same allowance. The
[CLI's client](https://github.com/modelstudioai/cli/blob/main/packages/core/src/client/client.ts)
renews the token with `GenerateCLIAccessToken`, retries once, and saves it
in its own profile. Magpie rereads the session, checks that its profile and
console scope still match, and reads the card with that session. Model API
keys cannot renew it. Browser-only profiles and profiles with a temporary
STS security token retain the manual sign-in path.

Renewals are serialized, bounded to eight seconds, and a failed attempt
is not repeated for the same session within a minute. Concurrent reads
reuse the session already renewed; a profile or console scope changed
during renewal returns an error. The CLI runs without a Terminal window,
with the selected configuration directory and profile pinned. Its output
is discarded, and no credentials are passed in process arguments. A
renewal failure cannot hide an expired session behind an old reading.
The CLI can save a replacement token and then fail its allowance retry.
Magpie still reads that saved session and reports its lookup result;
a workspace permission denial is not reported as an expired login or
another reason to renew.

Configure renewal credentials through the official CLI in the account
that owns the allowance, using a dedicated RAM user with only the required
permissions. The [personal-plan setup](https://help.aliyun.com/zh/model-studio/token-plan-personal-quick-start)
documents `AliyunTokenPlanReadOnlyAccess`, `AliyunBSSReadOnlyAccess`, and
the subscription permission in Bailian account management. The permission
to generate a CLI token must also be verified for that user. A real quota
read is required before treating the RAM user's access to the personal
allowance as verified. The console gateway can enforce additional Bailian
workspace permissions beyond the documented subscription role; a live
`BailianGateway.AuthorityPolicies.NoPermission` is shown with instructions
to check Bailian account management. Review any broader operation role
before granting it for a read-only allowance task. The CLI can remain
closed between renewals.

The card says **Bailian console**: this is that console account's shared
personal allowance, not a verified allowance for each saved API key. It is
shown once, on the first enabled provider using
`token-plan.maas.qianwenaiapi.com`; ordinary Qwen/DashScope and Coding Plan
endpoints are excluded. The reported fractional `per1MonthPercentage` is
shown as the monthly used or remaining percentage, with its reset time in
milliseconds. Older `per5HourPercentage` and `per1WeekPercentage` windows
are shown only when present. Missing fields never imply zero usage or an
unlimited plan. These console windows do not control individual keys'
routing, since the key and console account have not been matched. Readings
share the plan cache and per-card refresh; transient failures can show a
dated last reading of the same session, while an expired session cannot.

- Each refresh token has exactly one holder. Vendors rotate tokens on refresh, so two copies of one token would sign each other out. `savedTokenMu` stops two requests refreshing one saved account at once.
- A Claude subscription account is used only for Claude Code, through the genuine Claude Code binary (see [Claude subscription bridge](claude-subscription-bridge.md)).
- An unreadable `providers.json` is an error, never an empty list. Saving over an empty list would lose every provider.
- When `logins.json` cannot be decoded as saved accounts, reads keep the accounts read before. Before saving a change, `keepUnreadLogins` copies the original file aside, including valid JSON with invalid account field types, so its credentials can be recovered. If the existing file cannot be read or the backup fails, saving returns the error and leaves the original file unchanged. A missing file can be created normally.
- A switched-off provider (`Off`, #163) stays saved. The gateway says it is off instead of saying the model is unknown.
- A provider removed or switched off takes its models out of the catalog. [Agent wiring](agent-wiring.md)'s `Reseat` moves agents off them.
- A gateway key held to some accounts (#905) is held to those of the providers its list names: a provider it names no account of, the key uses as it always did. The list keeps an account by its stable id — `logins.json`'s `id`, set once written and kept through renames; the agents that keep no logins (and plugins) are known by a hash of the account's name, so an account renamed there drops out of a key's list, holding it closer, never wider. Storing an id for every agent is a larger change, not this one's.
- A sub2api key's windows are fixed: each starts at the key's first request after it was empty, aligned to the relay's day, and a window not started yet says `used 0` with no `reset_at`, so it is taken to renew a span from now. They count this key's spend only, not the relay's accounts behind it. `ForgetBalances` (a key or Balance URL changed) has every key's windows read again at its next ask, a read begun before dropped; until then the last reading stands, fresher than the card's, which routing's reads never write. A key changed is another key (`keyID`), with windows of its own.
- Whether a Codex account spends its credits (`CodexNoCredits` in settings, opt-out, by lower-cased email) is the gateway's routing only. It never changes which account Codex is signed in to: `NextLogin` moves off an account at its routing's spent share, 100% at most, credits or not.

## Verification

```sh
go test -tags nogui ./internal/provider -run 'TestAccount|TestCodex|TestGroup|TestQuota|TestLogin|TestSignIn|TestSeveralKeys|TestKeyProtocol|TestSetKeyWeight|TestReadSub2APIKeyLimits|TestSub2APIKeyLimitsOnItsCard|TestKeyAllowance|TestPlanKeyAllowance|TestPayAsYouGoKeyAskedSeldom'
go test -tags nogui ./internal/provider
go test -tags nogui ./internal/provider -run 'TestQwen|TestReadQwenPlan'
go test -tags nogui ./internal/provider -run 'TestVolcengine|TestReadVolcengine'
node --test internal/gui/tests/volcengine-balance.test.cjs
node --test internal/gui/tests/usage-card-refresh.test.cjs
```

The package's tests use a home of their own (`testenv`). Never point them at
real `~/.codex`, `~/.claude` or `~/.config/magpie` files. Check a change
against real accounts in a sandbox HOME, as [Code standards](../code-standards.md#acceptance-criteria) describes.
