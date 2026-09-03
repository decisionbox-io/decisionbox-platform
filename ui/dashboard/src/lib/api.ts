// All API calls use relative paths (/api/v1/...) — Next.js rewrites
// proxy them to the backend API server-side. No direct browser-to-API calls.
const API_BASE = '';

/**
 * ApiError is thrown by `request()` for every non-2xx response. The
 * `code` field carries the machine-readable error identifier the
 * backend sets (e.g. "embedding_not_configured", "context_overflow")
 * so callers can branch on condition without parsing message text.
 *
 * `details` is the upstream-sanitised explanation; surface it in a
 * "what happened" expander, not as the primary message.
 *
 * Callers that do `catch (err) { console.log(err.message) }` keep
 * working — ApiError extends Error and preserves the message field.
 */
export class ApiError extends Error {
  readonly status: number;
  readonly code?: string;
  readonly details?: string;

  constructor(message: string, status: number, code?: string, details?: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

/**
 * Machine-readable error codes the API returns on /ask. Keep this
 * union in sync with the constants exported from
 * services/api/internal/handler/response.go — the two pieces of code
 * cross the network, and the dashboard pattern-matches against the
 * exact strings.
 */
export type AskErrorCode =
  | 'embedding_not_configured'
  | 'llm_not_configured'
  | 'context_overflow'
  | 'llm_upstream'
  | 'llm_synthesis_failed';

/**
 * Maps an /ask error (typed ApiError or unknown) to the user-facing
 * sentence rendered in the chat transcript. Centralised here so:
 *
 *   1. The Ask page, any future "explain to me" toast, and the
 *      sessions list can render consistent copy.
 *   2. Adding a new typed code is a one-line change in the switch.
 *
 * Returns the generic "could not answer" string for any non-ApiError,
 * any ApiError without a recognised `code`, and any new code the
 * backend ships before this switch knows about it — surfacing raw
 * backend `message` text in those cases would leak provider internals
 * and break the consistent-copy contract above.
 */
export function askErrorMessage(err: unknown): string {
  const generic = 'Sorry, I could not answer this question. Try again, or start a new chat.';
  if (!(err instanceof ApiError)) {
    return generic;
  }
  switch (err.code as AskErrorCode | undefined) {
    case 'embedding_not_configured':
      return "This project has no embedding provider configured. Add one under project settings → Embedding to enable Ask.";
    case 'llm_not_configured':
      return "This project has no LLM provider configured. Add one under project settings → LLM to enable Ask.";
    case 'context_overflow':
      return "This conversation has grown past the model's context window. Start a new chat, or switch to a model with a wider context window.";
    case 'llm_upstream':
      // Per the ApiError doc, `details` is surfaced in a secondary
      // "what happened" expander, never inlined in the primary
      // sentence — otherwise a leaky upstream message becomes part
      // of the headline copy. Callers that want to render the
      // detail can read err.details directly off the ApiError.
      return 'The LLM provider rejected the request. Try again in a moment; if it keeps happening, check your provider credentials and quota.';
    case 'llm_synthesis_failed':
      return 'The LLM provider failed to answer this question. Try again, or start a new chat.';
    default:
      return generic;
  }
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const url = `${API_BASE}${path}`;

  const headers: Record<string, string> = {};
  // Only set Content-Type for requests with a body
  if (options?.body) {
    headers['Content-Type'] = 'application/json';
  }

  let res: Response;
  try {
    res = await fetch(url, {
      ...options,
      headers: { ...headers, ...options?.headers as Record<string, string> },
    });
  } catch (err) {
    throw new ApiError(
      `Cannot connect to DecisionBox API at ${API_BASE}. ` +
      `Make sure the API is running (make dev-api or docker compose up). ` +
      `Original error: ${(err as Error).message}`,
      0,
    );
  }

  // Some paths (Next.js proxy 502, misconfigured upstream returning
  // HTML 200, network blip) return non-JSON bodies. Guard against
  // res.json() throwing on either branch so the caller always sees a
  // meaningful ApiError instead of a silent `undefined` success or a
  // raw SyntaxError.
  let json: { data?: unknown; error?: string; code?: string; details?: string } = {};
  let parseFailed = false;
  try {
    json = await res.json();
  } catch {
    parseFailed = true;
  }

  if (!res.ok) {
    throw new ApiError(
      json.error || `API error: ${res.status}`,
      res.status,
      json.code,
      json.details,
    );
  }

  // 2xx with an unparseable body is treated as a server error rather
  // than returning `undefined` to the caller. Surfacing this loudly
  // makes the actual failure mode (misconfigured proxy / non-JSON
  // upstream) discoverable instead of producing mystery null derefs.
  if (parseFailed) {
    throw new ApiError(
      `API error: ${res.status} returned a non-JSON body`,
      res.status,
    );
  }

  return json.data as T;
}

// --- Types ---

export interface Domain {
  id: string;
  categories: Category[];
}

export interface Category {
  id: string;
  name: string;
  description: string;
}

export interface AnalysisArea {
  id: string;
  name: string;
  description: string;
  keywords: string[];
  is_base: boolean;
  priority: number;
}

// --- Domain Pack Types ---

export interface DomainPack {
  id: string;
  slug: string;
  name: string;
  description: string;
  version: string;
  author: string;
  source_url: string;
  is_published: boolean;
  categories: PackCategory[];
  prompts: PackPrompts;
  analysis_areas: PackAnalysisAreas;
  profile_schema: PackProfileSchema;
  created_at: string;
  updated_at: string;
}

export interface PackCategory {
  id: string;
  name: string;
  description: string;
}

export interface PackPrompts {
  base: {
    base_context: string;
    exploration: string;
    recommendations: string;
  };
  categories: Record<string, { exploration_context?: string }>;
}

export interface PackAnalysisArea {
  id: string;
  name: string;
  description: string;
  keywords: string[];
  priority: number;
  prompt: string;
}

export interface PackAnalysisAreas {
  base: PackAnalysisArea[];
  categories: Record<string, PackAnalysisArea[]>;
}

export interface PackProfileSchema {
  base: Record<string, unknown>;
  categories: Record<string, Record<string, unknown>>;
}

export interface PortableDomainPack {
  format: string;
  format_version: number;
  pack: DomainPack;
}

export interface Project {
  id: string;
  name: string;
  description: string;
  domain: string;
  category: string;
  warehouse: WarehouseConfig;
  llm: LLMConfig;
  embedding: EmbeddingConfig;
  blurb_llm?: BlurbLLMConfig;
  profile: Record<string, unknown>;
  // Output language for narrative fields (insight names/descriptions,
  // recommendation titles, /ask answers). Empty = legacy project,
  // treated as "English" by the agent and API. Allowed values are the
  // human-readable names rendered into prompt {{LANGUAGE}} substitutions
  // — e.g. "English", "Turkish", "German".
  language?: string;
  // Project lifecycle state. Empty = legacy project, treated as
  // "ready". Plugins may decode additional state strings via their
  // own model types — the community dashboard treats unknown values
  // as opaque.
  state?: string;
  status: string;
  // Latest-discovery-run summary, derived server-side at read time
  // (List/Get) from the discovery_runs collection — not stored on the
  // project. The homepage project cards render run state from these:
  //   last_run_status      — "" | "pending" | "running" | "completed"
  //                          | "failed" | "cancelled". Empty = the
  //                          project has never started a run.
  //   last_run_at          — when the most recent run started; the card
  //                          counts elapsed time up from this while it
  //                          is running.
  //   last_run_completed_at — when it finished; null while still
  //                          running (or never completed). Drives the
  //                          "Completed <timestamp>" label.
  last_run_status: string;
  last_run_at: string | null;
  last_run_completed_at?: string | null;
  // Schema-indexing lifecycle: "", "pending_indexing", "indexing", "ready", "failed".
  // Empty string = pre-migration (no index ever built).
  schema_index_status?: string;
  schema_index_error?: string;
  schema_index_updated_at?: string;
  // Per-project toggle for the LLM-native verifier + refuter pipeline.
  // Undefined → use deployment default (true). False → orchestrator
  // skips Phase 4.5/5.5 and stamps every doc with
  // combined: "validation_disabled". The validation panel reads the
  // *current* value of this field (not a snapshot from discovery time)
  // when deciding whether to show "Run validation" or "Disabled".
  validation_enabled?: boolean;
  // Undefined → default (true). Controls the analysis picker's smart
  // budget-overflow handling (dedup + "also examined" breadcrumb + tighter
  // re-compaction instead of dropping the lowest-scored steps). Only affects
  // runs whose evidence exceeds the model-window budget, so it is inert on
  // large-window models.
  smart_overflow_enabled?: boolean;
  // Undefined → off (opt-in). Model-agnostic "Enable reasoning": treats the
  // configured model as reasoning-capable for every provider — extra
  // window-budgeted exploration output headroom so a long hidden
  // chain-of-thought doesn't truncate the action, plus a reasoning hint on the
  // request (providers that wire native thinking act on it; others ignore it).
  reasoning_enabled?: boolean;
  // Undefined → default (true). Controls the discovery clarifying-questions
  // loop: after a run, the agent asks the analyst about anything it was
  // uncertain about, and the answers feed the next run. False opts the project
  // out (no questions generated).
  clarifying_questions_enabled?: boolean;
  // Undefined → default (true). Controls the LLM-generated suggested starter
  // questions shown on insight / recommendation pages ("Ask about this"). Makes
  // an automatic LLM call on page entry, so users can opt out in Settings.
  ask_suggestions_enabled?: boolean;
  // Which validation verdicts make an insight eligible for recommendation
  // generation. Undefined / empty → default {confirmed, supported} (the
  // historical filter). Selectable values: confirmed, supported, partial,
  // unverifiable, rejected. Insights whose validation never ran stay eligible
  // fail-open regardless.
  recommendation_verdicts?: string[];
  created_at: string;
  updated_at: string;
}

// Project lifecycle constants. Mirrors the Go-side
// models.ProjectState* constants; keep in sync when states are
// added or renamed.
export const PROJECT_STATE_READY = 'ready';

export interface BlurbLLMConfig {
  provider: string;
  model: string;
  config?: Record<string, string>;
}

export interface SchemaIndexProgress {
  phase: string; // "listing_tables" | "describing_tables" | "embedding"
  tables_total: number;
  tables_done: number;
  started_at?: string;
  updated_at?: string;
  error_message?: string;
  input_tokens?: number;
  output_tokens?: number;
}

export interface SchemaIndexStatus {
  status: string; // "", "pending_indexing", "indexing", "ready", "failed", "cancelled", "needs_reindex"
  error?: string;
  updated_at?: string;
  progress?: SchemaIndexProgress;
}

// SchemaCacheInfo is the wire shape for GET /schema-index/cache-info,
// rendered in Settings → Advanced next to the Clear button.
export interface SchemaCacheInfo {
  cached: boolean;
  last_cached_at?: string; // RFC 3339; empty when cached=false
}

// EmbeddingLiveModel is one row from the embedding live-list endpoint.
// Mirrors LiveModel (LLM) but with dimensions instead of wire — that's
// the dimension badge the dashboard renders in the picker, and the
// field that actually matters for Qdrant collection compatibility.
export interface EmbeddingLiveModel {
  id: string;
  display_name: string;
  dimensions: number;
  lifecycle?: string;
  source: 'catalog' | 'live' | 'both';
}

export interface EmbeddingLiveModelsResponse {
  models: EmbeddingLiveModel[];
  live_error?: string;
}

export interface SchemaIndexLogLine {
  run_id: string;
  line: string;
  created_at: string;
}

export interface EmbeddingConfig {
  provider: string;
  model: string;
  /** Per-provider non-credential settings (auth_method, role_arn, …). */
  config?: Record<string, string>;
}

export interface WarehouseConfig {
  provider: string;
  project_id: string;
  datasets: string[];
  location: string;
  filter_field: string;
  filter_value: string;
  config?: Record<string, string>; // provider-specific: workgroup, database, region, cluster_id, etc.
}

export interface LLMConfig {
  provider: string;
  model: string;
  config?: Record<string, string>; // provider-specific: project_id, location, host, etc.
}

export interface DiscoveryResult {
  id: string;
  project_id: string;
  domain: string;
  category: string;
  run_type: string;
  areas_requested: string[];
  discovery_date: string;
  total_steps: number;
  duration: number;
  insights: Insight[];
  recommendations: Recommendation[];
  summary: Summary;
  // Logs are no longer embedded — fetch them from the dedicated endpoints
  // when needed (each has its own GET under /api/v1/discoveries/{id}/...).
  // The previous embedded arrays hit the 16MB BSON document limit on long
  // runs and killed the discovery save.
  created_at: string;
}

export interface Insight {
  id: string;
  analysis_area: string;
  name: string;
  description: string;
  // Markdown rendition of `description`, rendered formatted on the detail
  // view. `description` stays plain text. Absent on unformatted/legacy insights.
  description_md?: string;
  severity: string;
  affected_count: number;
  risk_score: number;
  confidence: number;
  metrics: Record<string, unknown>;
  indicators: string[];
  target_segment: string;
  source_steps?: number[];
  validation?: InsightValidation;
  discovered_at: string;
}

// Status values come from the seven-status taxonomy plus the legacy
// values older docs may still carry. The dashboard sorts / filters
// on `combined` (the canonical field on new docs); the older
// `status` field is only meaningful on legacy docs.
export type ValidationStatus =
  | "confirmed"
  | "supported"
  | "rejected"
  | "partial"
  | "unverifiable"
  | "validation_disabled"
  | "skipped_budget_cap"
  // Legacy values:
  | "adjusted"
  | "unverified"
  | "error"
  | "";

export type ValidationDocKind = "insight" | "recommendation";

export type ValidationAgentMode = "verifier" | "refuter";

export interface ClaimEvidence {
  kind: "step_row" | "warehouse_query" | "none" | "";
  step_id?: number;
  query_sql?: string;
  row?: Record<string, unknown>;
  additional_rows?: number;
}

export interface ClaimVerdict {
  claim_text: string;
  claim_kind: string;
  is_headline: boolean;
  status: ValidationStatus;
  reasoning: string;
  evidence: ClaimEvidence;
}

export interface StructuredVerdict {
  doc_id: string;
  doc_kind: ValidationDocKind;
  mode: ValidationAgentMode;
  claims_considered: string[];
  claim_verdicts: ClaimVerdict[];
  overall: ValidationStatus;
  overall_reason: string;
  lookups_used: number;
  queries_issued: number;
  step_reads_used: number;
  llm_tokens_in: number;
  llm_tokens_out: number;
  duration_millis: number;
}

export interface InsightValidation {
  // --- legacy fields ---
  status?: string;
  verified_count?: number;
  original_count?: number;
  query?: string;
  reasoning?: string;
  validated_at?: string;
  input_tokens?: number;
  output_tokens?: number;
  // --- new fields ---
  // warehouse_id is the datasource this doc was verified against
  // (multi-warehouse); empty on single-warehouse projects.
  warehouse_id?: string;
  verifier?: StructuredVerdict;
  refuter?: StructuredVerdict;
  combined?: ValidationStatus;
  refuter_disabled?: boolean;
}

export interface Recommendation {
  id: string;
  category: string;
  title: string;
  description: string;
  // Markdown rendition of `description`. `description` stays plain text.
  // Absent on unformatted/legacy recommendations.
  description_md?: string;
  priority: number;
  target_segment: string;
  segment_size: number;
  expected_impact: { metric: string; estimated_improvement: string; reasoning: string };
  actions: string[];
  related_insight_ids?: string[];
  confidence: number;
  validation?: InsightValidation;
}

export interface Summary {
  text: string;
  key_findings: string[];
  top_recommendations: string[];
  total_insights: number;
  total_recommendations: number;
  queries_executed: number;
  errors?: string[];
}

export interface ExplorationStep {
  step: number;
  timestamp: string;
  action: string;
  thinking: string;
  query_purpose: string;
  query: string;
  row_count: number;
  execution_time_ms: number;
  error: string;
  fixed: boolean;
  // warehouse_id is the datasource this step's query ran against on a
  // multi-warehouse project (empty/absent on a single-warehouse project).
  warehouse_id?: string;
}

export interface AnalysisLogStep {
  area_id: string;
  area_name: string;
  run_at: string;
  relevant_queries: number;
  query_results_chars?: number;
  selected_steps?: SelectedStep[];
  dropped_steps?: DroppedAnalysisStep[];
  tokens_in: number;
  tokens_out: number;
  duration_ms: number;
  insight_count: number;
  error: string;
}

export interface SelectedStep {
  step: number;
  score: number;
  source: string; // "vector" | "exact_match"
}

export interface DroppedAnalysisStep {
  step: number;
  score: number;
  reason: string; // "below_min_score" | "over_budget"
}

// Manual-validation job — one queued / running / completed
// run-validation request. Mirrors services/api/models/validation_job.go.
// Status lifecycle: pending → running → completed | failed | cancelled.
export type ValidationJobStatus =
  | 'pending'
  | 'running'
  | 'completed'
  | 'failed'
  | 'cancelled';

export type ValidationJobStep = 'verifier' | 'refuter' | 'combining';

export interface ValidationJob {
  id: string;
  project_id: string;
  discovery_id: string;
  doc_kind: ValidationDocKind;
  doc_id: string;
  status: ValidationJobStatus;
  step?: ValidationJobStep;
  error?: string;
  attempt: number;
  requested_by?: string;
  worker_id?: string;
  enqueued_at: string;
  claimed_at?: string;
  started_at?: string;
  heartbeat_at?: string;
  completed_at?: string;
  cancelled_at?: string;
  run_id?: string;
}

export interface ValidationLogEntry {
  insight_id: string;
  analysis_area: string;
  claimed_count: number;
  verified_count: number;
  status: string;
  reasoning: string;
  query: string;
  validated_at: string;
  // --- new fields ---
  // warehouse_id is the datasource this doc was verified against
  // (multi-warehouse); empty on single-warehouse projects.
  warehouse_id?: string;
  doc_kind?: ValidationDocKind;
  verifier?: StructuredVerdict;
  refuter?: StructuredVerdict;
  combined?: ValidationStatus;
  refuter_disabled?: boolean;
}

export interface ProjectStatus {
  project_id: string;
  run?: DiscoveryRunStatus;
  last_discovery?: {
    date: string;
    insights_count: number;
    total_steps: number;
  };
}

export interface ProjectPrompts {
  exploration: string;
  recommendations: string;
  base_context: string;
  analysis_areas: Record<string, AnalysisAreaConfig>;
}

export interface AnalysisAreaConfig {
  name: string;
  description: string;
  keywords: string[];
  prompt: string;
  is_base: boolean;
  is_custom: boolean;
  priority: number;
  enabled: boolean;
}

export interface ProviderMeta {
  id: string;
  name: string;
  description: string;
  /** Short SQL dialect label (warehouse providers only), e.g. "BigQuery Standard SQL", "T-SQL". */
  dialect?: string;
  config_fields: ConfigField[];
  auth_methods?: AuthMethod[];
  models?: ModelInfo[];
}

export interface ModelInfo {
  id: string;
  display_name: string;
  wire: string; // "anthropic" | "openai-compat" | "google-native" | "" (unknown)
  max_input_tokens?: number;
  max_output_tokens?: number;
  input_price_per_million?: number;
  output_price_per_million?: number;
  // Lifecycle from the upstream list endpoint when available —
  // e.g. "ACTIVE" / "LEGACY" on Bedrock. Empty when the upstream
  // does not expose it or the row came from our shipped catalog.
  lifecycle?: string;
}

// LiveModel extends ModelInfo with two derived fields:
//   source       — where the row came from: "catalog" (only in our
//                  shipped catalog), "live" (only in the upstream),
//                  "both" (matched).
//   dispatchable — true iff DecisionBox has a wire implementation
//                  for this model. Live rows whose family we don't
//                  implement (Nova, Titan, Cohere, …) come back with
//                  dispatchable=false so the UI can grey them out.
export interface LiveModel extends ModelInfo {
  source: 'catalog' | 'live' | 'both';
  dispatchable: boolean;
  // Limits the upstream live endpoint actually reported (never a catalog
  // estimate). Present on any row — including "both" — whose values came from
  // live detection; absent when only the catalog knew them. The dashboard
  // prefills the operator override fields only from these, so a catalog value
  // is never pinned as a manual override.
  live_max_input_tokens?: number;
  live_max_output_tokens?: number;
}

export interface LiveModelsResponse {
  models: LiveModel[];
  live_error?: string;
}

export interface AuthMethod {
  id: string;
  name: string;
  description: string;
  fields: ConfigField[];
}

export interface ConfigField {
  key: string;
  label: string;
  description: string;
  required: boolean;
  type: string;
  default: string;
  placeholder: string;
  options?: ConfigOption[];
  free_text?: boolean;
}

export interface ConfigOption {
  value: string;
  label: string;
}

export interface DiscoveryRunStatus {
  id: string;
  project_id: string;
  status: string; // pending, running, completed, failed
  phase: string;
  phase_detail: string;
  progress: number; // 0-100
  started_at: string;
  updated_at: string;
  completed_at: string | null;
  error: string;
  // Per-step rows live in the discovery_run_steps collection now and are
  // streamed via api.listRunSteps(runId, sinceID). The legacy embedded
  // `steps` array is gone — see PR splitting them out for the 16MB
  // BSON limit.
  total_queries: number;
  successful_queries: number;
  failed_queries: number;
  insights_found: number;
  schema_tokens?: number;
  schema_table_count?: number;
  schema_lookup_calls?: number;
  schema_search_calls?: number;
  analysis_step_index_upserts?: number;
  analysis_step_index_search_calls?: number;
  analysis_steps_dropped?: number;
}

// RunStep is one row in the live run-step stream. `id` is the opaque
// cursor — the dashboard echoes the last row's `id` back as `since`
// on the next poll. The other fields are the per-step semantic data.
export interface RunStep {
  id: string;
  phase: string;
  step_num: number;
  timestamp: string;
  type: string; // phase_start, query, analysis, insight, error, info
  message: string;
  llm_thinking: string;
  query: string;
  query_result: string;
  row_count: number;
  query_time_ms: number;
  query_fixed: boolean;
  // warehouse_id is the datasource this step's query ran against
  // (multi-warehouse); empty on non-query steps + single-warehouse runs.
  warehouse_id?: string;
  insight_name: string;
  insight_severity: string;
  error: string;
  input_tokens?: number;
  output_tokens?: number;
}

// DebugLogEntry mirrors services/api/models/DebugLogEntry — the lean,
// public-safe projection of an agent debug log event. The server
// withholds raw LLM system prompts and raw query result rows (those stay
// in Mongo); LLM responses are included but truncated to ~4KB (UTF-8-safe).
// Safe to render directly in the UI.
export interface DebugLogEntry {
  id: string;
  discovery_run_id: string;
  created_at: string;
  log_type: string;
  component: string;
  operation: string;
  phase?: string;
  step?: number;
  duration_ms?: number;
  success: boolean;
  // SQL fields (present for execute_query). `sql_query_fixed` is set when
  // the SQL fixer rewrote the query on retry — the executed query is
  // `sql_query_fixed` if non-empty, otherwise `sql_query`.
  sql_query?: string;
  sql_query_fixed?: string;
  query_purpose?: string;
  row_count?: number;
  fix_attempts?: number;
  query_error?: string;
  // LLM fields (present for create_message) — response is capped
  // server-side to keep polls cheap; look at the ...[truncated] suffix.
  llm_model?: string;
  llm_response?: string;
  llm_input_tokens?: number;
  llm_output_tokens?: number;
  error_message?: string;
}

export interface Feedback {
  id: string;
  project_id: string;
  discovery_id: string;
  target_type: 'insight' | 'recommendation' | 'exploration_step';
  target_id: string;
  rating: 'like' | 'dislike';
  comment?: string;
  created_at: string;
}

// Discovery clarifying questions — the agent's post-run questions to the analyst.
export type QuestionAnswerType = 'boolean' | 'single_choice' | 'multi_choice' | 'free_text';
export type QuestionTargetType = 'insight' | 'recommendation' | 'table' | 'area';

export interface QuestionOption {
  id: string;
  label: string;
}

export interface DiscoveryQuestion {
  id: string;
  project_id: string;
  run_id: string;
  discovery_id: string;
  question: string;
  rationale: string;
  linked_target: { type: QuestionTargetType; id: string };
  answer_type: QuestionAnswerType;
  options?: QuestionOption[];
  status: 'pending' | 'answered' | 'dismissed';
  answer?: string;
  answer_option_ids?: string[];
  answer_note?: string;
  // Populated once the question is resolved. answer_source_id links the answer
  // to the knowledge-base note it created, so the review surfaces can point the
  // analyst at the note they can edit.
  answered_by?: string;
  answered_at?: string;
  answer_source_id?: string;
  created_at: string;
  updated_at?: string;
}

// Payload for answering: send only the fields the answer_type needs; the server
// derives the canonical answer text and materializes the KB note.
export interface QuestionAnswerPayload {
  answer_bool?: boolean;
  answer_option_ids?: string[];
  answer?: string;
  answer_note?: string;
}

export interface CostEstimate {
  llm: { provider: string; model: string; estimated_input_tokens: number; estimated_output_tokens: number; cost_usd: number };
  warehouse: { provider: string; estimated_queries: number; estimated_bytes_scanned: number; cost_usd: number };
  total_cost_usd: number;
  breakdown: { exploration: number; analysis: number; validation: number; recommendations: number };
}

export interface Pricing {
  id?: string;
  llm: Record<string, Record<string, { input_per_million: number; output_per_million: number }>>;
  warehouse: Record<string, { cost_model: string; cost_per_tb_scanned_usd: number }>;
}

export interface SecretEntryResponse {
  key: string;
  masked: string;
  updated_at: string;
  warning?: string;
}

export interface TestConnectionResult {
  success: boolean;
  error?: string;
  provider?: string;
  model?: string;
  datasets?: string[];
}

// --- Vector Search Types ---

export interface EmbeddingProviderMeta {
  id: string;
  name: string;
  description: string;
  config_fields: ConfigField[];
  auth_methods?: AuthMethod[];
  models: EmbeddingModelMeta[];
}

export interface EmbeddingModelMeta {
  id: string;
  name: string;
  dimensions: number;
}

export interface SearchRequest {
  query: string;
  types?: string[];
  limit?: number;
  min_score?: number;
  filters?: { severity?: string; analysis_area?: string };
}

export interface CrossProjectSearchRequest {
  query: string;
  embedding_model: string;
  types?: string[];
  limit?: number;
  min_score?: number;
}

export interface SearchResultItem {
  id: string;
  // 'source_chunk' is returned when an answer cites a knowledge-source
  // document (an upload, URL, or a file synced from external storage)
  // rather than a discovery finding. Such a citation has no
  // discovery_id, and links to the project's knowledge sources instead
  // of an insight or recommendation — callers switching on this field
  // must handle it rather than defaulting it to a recommendation.
  type: 'insight' | 'recommendation' | 'source_chunk';
  score: number;
  name: string;
  title?: string;
  description: string;
  severity?: string;
  analysis_area?: string;
  discovery_id: string;
  discovered_at: string;
  project_id?: string;
  project_name?: string;
  validation?: InsightValidation;
}

export interface SearchResponse {
  results: SearchResultItem[];
  embedding_model: string;
  projects_searched?: number;
  projects_excluded?: number;
}

// SeedContext anchors an Ask conversation to one insight / recommendation the
// user launched "Ask about this" from. The client passes {type,id,title} (+ an
// optional description fallback); the server hydrates the authoritative
// grounding text by id.
export interface SeedContext {
  type: 'insight' | 'recommendation';
  id: string;
  title: string;
  description?: string;
}

export interface AskRequest {
  question: string;
  limit?: number;
  session_id?: string;
  // Sent on the first turn of a seeded conversation. The server hydrates + then
  // persists it on the session so follow-ups stay grounded.
  seed_context?: { type: string; id: string; text?: string };
}

export interface AskSuggestionsResponse {
  questions: string[];
}

export interface AskResponse {
  answer: string;
  sources: SearchResultItem[];
  model: string;
  session_id: string;
  input_tokens?: number;
  output_tokens?: number;
}

export interface AskSession {
  id: string;
  project_id: string;
  user_id: string;
  title: string;
  messages: AskSessionMessage[];
  message_count: number;
  created_at: string;
  updated_at: string;
}

export interface AskSessionMessage {
  question: string;
  answer: string;
  sources: AskSessionSource[];
  model: string;
  input_tokens?: number;
  output_tokens?: number;
  created_at: string;
}

export interface AskSessionSource {
  id: string;
  type: string;
  name: string;
  score: number;
  severity?: string;
  analysis_area?: string;
  description?: string;
  discovery_id: string;
}

export interface StandaloneInsight {
  id: string;
  project_id: string;
  discovery_id: string;
  domain: string;
  category: string;
  analysis_area: string;
  name: string;
  description: string;
  severity: string;
  affected_count: number;
  risk_score: number;
  confidence: number;
  metrics: Record<string, unknown>;
  indicators: string[];
  target_segment: string;
  source_steps?: number[];
  validation?: InsightValidation;
  embedding_text?: string;
  embedding_model?: string;
  duplicate_of?: string;
  similarity_score?: number;
  discovered_at: string;
  created_at: string;
}

export interface StandaloneRecommendation {
  id: string;
  project_id: string;
  discovery_id: string;
  domain: string;
  category: string;
  recommendation_category: string;
  title: string;
  description: string;
  priority: number;
  target_segment: string;
  segment_size: number;
  expected_impact: { metric: string; estimated_improvement: string; reasoning?: string };
  actions: string[];
  related_insight_ids?: string[];
  confidence: number;
  validation?: InsightValidation;
  embedding_text?: string;
  embedding_model?: string;
  duplicate_of?: string;
  similarity_score?: number;
  created_at: string;
}

export interface SearchHistoryEntry {
  id: string;
  user_id: string;
  project_id: string;
  query: string;
  type: 'search' | 'ask';
  results_count: number;
  top_result_ids?: string[];
  top_result_score?: number;
  answer_summary?: string;
  source_ids?: string[];
  llm_model?: string;
  input_tokens?: number;
  output_tokens?: number;
  created_at: string;
}

// --- Bookmark List / Bookmark / Read Mark types ---

export interface BookmarkList {
  id: string;
  project_id: string;
  user_id: string;
  name: string;
  description?: string;
  color?: string;
  created_at: string;
  updated_at: string;
  item_count: number;
}

export interface Bookmark {
  id: string;
  list_id: string;
  project_id: string;
  user_id: string;
  discovery_id: string;
  target_type: 'insight' | 'recommendation';
  target_id: string;
  note?: string;
  created_at: string;
}

// BookmarkItem is a bookmark joined with its resolved target. When the source
// insight or recommendation has been deleted, `deleted` is true and `target` is
// undefined — the UI should render a "[removed]" placeholder.
export interface BookmarkItem {
  bookmark: Bookmark;
  target?: StandaloneInsight | StandaloneRecommendation;
  deleted?: boolean;
}

export interface BookmarkListWithItems extends BookmarkList {
  items: BookmarkItem[];
}

// System inventory (GET /api/v1/system). `kind` is open-ended on purpose
// — the System page renders whatever the registry reports, so a build that
// registers a new kind still displays. Keep these fields in sync with
// libs/go-common/systeminfo.Descriptor.
export type SystemComponentKind = 'service' | 'worker' | (string & {});

export interface SystemComponent {
  name: string;
  kind: SystemComponentKind;
  version?: string;
  commit?: string;
  build_date?: string;
  // Parent service a worker runs inside (e.g. "API"); empty for services.
  runs_in?: string;
  // Clarification — e.g. that a worker shares its parent image's version.
  note?: string;
}

export interface SystemInfo {
  components: SystemComponent[];
}

// AppConfig carries deployment-level capability flags the dashboard reads
// once at load. ai_config_managed is true when this deployment routes all
// inference through a managed gateway (server-side AI_GATEWAY_URL set): the
// AI/Embedding + Blurb settings tabs and the AI/embedding/blurb new-project
// wizard steps are hidden because the config is preset and immutable
// server-side. The server, not the UI, is authoritative — this only drives
// what we bother rendering.
export interface AppConfig {
  ai_config_managed: boolean;
}

// --- API Functions ---

export const api = {
  // Providers (dynamic — registered in Go via init())
  listLLMProviders: () => request<ProviderMeta[]>('/api/v1/providers/llm'),
  listLiveLLMModels: (providerID: string, config: Record<string, string>) =>
    request<LiveModelsResponse>(`/api/v1/providers/llm/${encodeURIComponent(providerID)}/models/live`, {
      method: 'POST',
      body: JSON.stringify({ config }),
    }),
  // slot defaults to "llm" on the server. Pass "blurb_llm" to read the
  // project's blurb-LLM slot (project.blurb_llm + blurb-llm-credentials),
  // which falls back to the analysis-LLM slot when no blurb override is
  // configured (matching the agent's resolveBlurbLLM behaviour).
  listLiveLLMModelsForProject: (projectID: string, slot?: 'llm' | 'blurb_llm') =>
    request<LiveModelsResponse>(`/api/v1/projects/${encodeURIComponent(projectID)}/providers/llm/models/live`, {
      method: 'POST',
      body: JSON.stringify(slot ? { slot } : {}),
    }),
  listWarehouseProviders: () => request<ProviderMeta[]>('/api/v1/providers/warehouse'),

  // System inventory — component + worker versions (System page)
  getSystemInfo: () => request<SystemInfo>('/api/v1/system'),

  // Deployment capability flags (drives which config surfaces the UI renders)
  getAppConfig: () => request<AppConfig>('/api/v1/config'),

  // Domain Packs (CRUD)
  listDomainPacks: () => request<DomainPack[]>('/api/v1/domain-packs'),
  getDomainPack: (slug: string) => request<DomainPack>(`/api/v1/domain-packs/${slug}`),
  createDomainPack: (pack: Partial<DomainPack>) =>
    request<DomainPack>('/api/v1/domain-packs', { method: 'POST', body: JSON.stringify(pack) }),
  updateDomainPack: (slug: string, pack: Partial<DomainPack>) =>
    request<DomainPack>(`/api/v1/domain-packs/${slug}`, { method: 'PUT', body: JSON.stringify(pack) }),
  deleteDomainPack: (slug: string) =>
    request<{ deleted: string }>(`/api/v1/domain-packs/${slug}`, { method: 'DELETE' }),
  importDomainPack: (data: PortableDomainPack) =>
    request<DomainPack>('/api/v1/domain-packs/import', { method: 'POST', body: JSON.stringify(data) }),
  exportDomainPack: (slug: string) =>
    request<PortableDomainPack>(`/api/v1/domain-packs/${slug}/export`),

  // Domains
  listDomains: () => request<Domain[]>('/api/v1/domains'),
  listCategories: (domain: string) => request<Category[]>(`/api/v1/domains/${domain}/categories`),
  getProfileSchema: (domain: string, category: string) =>
    request<Record<string, unknown>>(`/api/v1/domains/${domain}/categories/${category}/schema`),
  getAnalysisAreas: (domain: string, category: string) =>
    request<AnalysisArea[]>(`/api/v1/domains/${domain}/categories/${category}/areas`),

  // Projects
  createProject: (data: Partial<Project>) =>
    request<Project>('/api/v1/projects', { method: 'POST', body: JSON.stringify(data) }),
  listProjects: () => request<Project[]>('/api/v1/projects'),
  getProject: (id: string) => request<Project>(`/api/v1/projects/${id}`),
  updateProject: (id: string, data: Partial<Project>) =>
    request<Project>(`/api/v1/projects/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  // deleteProject performs a full cascade delete (Mongo, Qdrant, and
  // mongo-backed secrets). `secrets_skipped: true` means an external
  // secret manager (GCP/AWS/Azure) is configured and the user must
  // clean up warehouse credentials via the cloud console themselves.
  // 409 from the server when a schema-indexing run is in flight —
  // user must cancel that first via POST .../schema-index/cancel.
  deleteProject: (id: string) =>
    request<{ deleted: string; secrets_skipped: boolean }>(
      `/api/v1/projects/${id}`,
      { method: 'DELETE' }
    ),

  // Prompts
  getPrompts: (projectId: string) =>
    request<ProjectPrompts>(`/api/v1/projects/${projectId}/prompts`),
  updatePrompts: (projectId: string, prompts: ProjectPrompts) =>
    request<ProjectPrompts>(`/api/v1/projects/${projectId}/prompts`, { method: 'PUT', body: JSON.stringify(prompts) }),

  // Schema indexing lifecycle
  //
  // Status / retry / reindex surface the background worker loop that
  // builds per-project schema vectors in Qdrant. Dashboard polls status
  // every 2s while the project-detail page is live; POST retry is only
  // valid from `failed`; POST reindex forces a full rebuild from any
  // state (drops the collection + flips to pending_indexing).
  getSchemaIndexStatus: (projectId: string) =>
    request<SchemaIndexStatus>(`/api/v1/projects/${projectId}/schema-index/status`),
  retrySchemaIndex: (projectId: string) =>
    request<{ status: string }>(`/api/v1/projects/${projectId}/schema-index/retry`, { method: 'POST' }),
  // cancelSchemaIndex signals the in-flight indexing run to stop. The
  // status transition (→ "cancelled") happens asynchronously once the
  // worker sees the cancel via context — the dashboard's 2s status
  // poll picks it up.
  cancelSchemaIndex: (projectId: string) =>
    request<{ status: string }>(`/api/v1/projects/${projectId}/schema-index/cancel`, { method: 'POST' }),
  reindexSchema: (projectId: string) =>
    request<{ status: string }>(`/api/v1/projects/${projectId}/reindex`, { method: 'POST' }),
  // invalidateSchemaCache drops the project_schema_cache rows so the next
  // indexing run skips the cache and rediscovers from the warehouse.
  // Used by Settings → Advanced "Clear schema cache". 409 when an
  // indexing run is in flight; 503 when the API instance has no cache
  // wired (Qdrant-less builds).
  invalidateSchemaCache: (projectId: string) =>
    request<{ status: string }>(
      `/api/v1/projects/${projectId}/schema-index/invalidate-cache`,
      { method: 'POST' }
    ),
  // getSchemaCacheInfo returns metadata about the project's schema
  // cache (last_cached_at, cached). Used by Settings → Advanced to
  // render "Last cached: …" next to the Clear button.
  getSchemaCacheInfo: (projectId: string) =>
    request<SchemaCacheInfo>(
      `/api/v1/projects/${projectId}/schema-index/cache-info`
    ),
  // listSchemaIndexLogs returns agent stderr lines captured during
  // indexing runs. `since` (ISO 8601) cursor keeps the tail poll cheap —
  // only lines newer than the timestamp come back.
  listSchemaIndexLogs: (projectId: string, since?: string, limit?: number) => {
    const params = new URLSearchParams();
    if (since) params.set('since', since);
    if (limit) params.set('limit', String(limit));
    const qs = params.toString();
    return request<SchemaIndexLogLine[]>(
      `/api/v1/projects/${projectId}/schema-index/logs${qs ? '?' + qs : ''}`
    );
  },

  // Discovery
  //
  // min_steps: optional floor on exploration steps before the agent will
  // accept a "done" signal from the LLM. Omitted → server applies default
  // (60% of max_steps). 0 → explicitly disabled. > max_steps → 400 error.
  // Recommended for reasoning models (Qwen3, DeepSeek-R1, GPT-OSS) that
  // tend to terminate exploration too early.
  triggerDiscovery: (projectId: string, options?: { areas?: string[]; max_steps?: number; min_steps?: number }) =>
    request<{ status: string; message: string; run_id?: string }>(`/api/v1/projects/${projectId}/discover`, {
      method: 'POST',
      body: options ? JSON.stringify(options) : undefined,
    }),
  getRun: (runId: string) =>
    request<DiscoveryRunStatus>(`/api/v1/runs/${runId}`),
  cancelRun: (runId: string) =>
    request<{ status: string; message: string }>(`/api/v1/runs/${runId}`, { method: 'DELETE' }),
  // getDebugLogs returns the lean projection of `discovery_debug_logs` the
  // agent writes for a run. The "since" arg is the ISO timestamp of the
  // newest entry already rendered — the UI passes it on each poll so the
  // server only returns what's new, making the tailing panel idempotent.
  getDebugLogs: (runId: string, since?: string, limit?: number) => {
    const params = new URLSearchParams();
    if (since) params.set('since', since);
    if (limit) params.set('limit', String(limit));
    const qs = params.toString();
    return request<DebugLogEntry[]>(`/api/v1/runs/${runId}/debug-logs${qs ? '?' + qs : ''}`);
  },
  listDiscoveries: (projectId: string) =>
    request<DiscoveryResult[]>(`/api/v1/projects/${projectId}/discoveries`),
  getLatestDiscovery: (projectId: string) =>
    request<DiscoveryResult>(`/api/v1/projects/${projectId}/discoveries/latest`),
  getDiscoveryById: (discoveryId: string) =>
    request<DiscoveryResult>(`/api/v1/discoveries/${discoveryId}`),
  // Per-discovery split-log endpoints. The previous embedded
  // exploration_log / analysis_log / validation_log arrays were removed
  // from the DiscoveryResult document to keep it under Mongo's 16MB
  // limit; these are how the dashboard rehydrates the LLM dialog.
  listExplorationSteps: (discoveryId: string, limit?: number) => {
    const qs = limit ? `?limit=${limit}` : '';
    return request<ExplorationStep[]>(`/api/v1/discoveries/${discoveryId}/exploration-steps${qs}`);
  },
  listAnalysisSteps: (discoveryId: string) =>
    request<AnalysisLogStep[]>(`/api/v1/discoveries/${discoveryId}/analysis-steps`),
  listValidationResults: (discoveryId: string) =>
    request<ValidationLogEntry[]>(`/api/v1/discoveries/${discoveryId}/validation-results`),

  // Manual-validation jobs — enqueue / cancel / list.
  // listValidationJobs returns 0..N rows most-recent-first; the
  // dashboard router picks the most-recent non-terminal one to drive
  // the in-flight progress card.
  listValidationJobs: (discoveryId: string, docKind: ValidationDocKind, docId: string) => {
    const qs = new URLSearchParams({ doc_kind: docKind, doc_id: docId }).toString();
    return request<ValidationJob[]>(`/api/v1/discoveries/${discoveryId}/validation-jobs?${qs}`);
  },
  enqueueValidateInsight: (discoveryId: string, insightId: string) =>
    request<ValidationJob>(`/api/v1/discoveries/${discoveryId}/insights/${insightId}/validate`, { method: 'POST' }),
  enqueueValidateRecommendation: (discoveryId: string, recommendationId: string) =>
    request<ValidationJob>(`/api/v1/discoveries/${discoveryId}/recommendations/${recommendationId}/validate`, { method: 'POST' }),
  // Cancel returns a small JSON envelope ({status: "cancelled"})
  // rather than 204 — the shared request() helper rejects non-JSON
  // bodies on success, which would surface a misleading "Cancel
  // failed" toast for every successful cancel.
  cancelValidationJob: (jobId: string) =>
    request<{ status: string }>(`/api/v1/validation-jobs/${jobId}/cancel`, { method: 'POST' }),
  getRecommendationLog: (discoveryId: string) =>
    request<{ run_at: string; insight_count: number; tokens_in?: number; tokens_out?: number; duration_ms?: number; status?: string; recommendations_dropped?: number; recommendations_dropped_parse?: number; recommendation_parse_retries?: number; error?: string }>(`/api/v1/discoveries/${discoveryId}/recommendation-log`),
  // Live run-step stream. The dashboard polls with the last `id` it
  // has rendered (opaque cursor); the server returns rows strictly
  // after that id, ordered by ObjectID. ms-precision timestamps would
  // collide and silently drop rows — see services/api/database/run_step_repo.go.
  listRunSteps: (runId: string, sinceID?: string, limit?: number) => {
    const params = new URLSearchParams();
    if (sinceID) params.set('since', sinceID);
    if (limit) params.set('limit', String(limit));
    const qs = params.toString();
    return request<RunStep[]>(`/api/v1/runs/${runId}/steps${qs ? '?' + qs : ''}`);
  },
  getDiscoveryByDate: (projectId: string, date: string) =>
    request<DiscoveryResult>(`/api/v1/projects/${projectId}/discoveries/${date}`),
  getProjectStatus: (projectId: string) =>
    request<ProjectStatus>(`/api/v1/projects/${projectId}/status`),

  // Feedback
  submitFeedback: (discoveryId: string, data: { project_id?: string; target_type: string; target_id: string; rating: string; comment?: string }) =>
    request<Feedback>(`/api/v1/discoveries/${discoveryId}/feedback`, { method: 'POST', body: JSON.stringify(data) }),
  listFeedback: (discoveryId: string) =>
    request<Feedback[]>(`/api/v1/discoveries/${discoveryId}/feedback`),
  deleteFeedback: (feedbackId: string) =>
    request<{ status: string }>(`/api/v1/feedback/${feedbackId}`, { method: 'DELETE' }),

  // Discovery clarifying questions (enterprise-backed; empty on community builds
  // where the routes 404 — callers .catch(() => []) so the panel just hides).
  listProjectQuestions: (projectId: string, opts?: { status?: string; discovery_id?: string }) => {
    const qs = new URLSearchParams();
    if (opts?.status) qs.set('status', opts.status);
    if (opts?.discovery_id) qs.set('discovery_id', opts.discovery_id);
    const suffix = qs.toString() ? `?${qs.toString()}` : '';
    return request<DiscoveryQuestion[]>(`/api/v1/projects/${projectId}/discovery-questions${suffix}`);
  },
  answerQuestion: (projectId: string, questionId: string, data: QuestionAnswerPayload) =>
    request<DiscoveryQuestion>(`/api/v1/projects/${projectId}/discovery-questions/${questionId}/answer`, {
      method: 'POST', body: JSON.stringify(data),
    }),
  dismissQuestion: (projectId: string, questionId: string) =>
    request<DiscoveryQuestion>(`/api/v1/projects/${projectId}/discovery-questions/${questionId}/dismiss`, {
      method: 'POST',
    }),

  // Cost estimation
  estimateCost: (projectId: string, opts?: { areas?: string[]; max_steps?: number }) =>
    request<CostEstimate>(`/api/v1/projects/${projectId}/discover/estimate`, {
      method: 'POST', body: opts ? JSON.stringify(opts) : undefined,
    }),

  // Pricing
  getPricing: () => request<Pricing>('/api/v1/pricing'),
  updatePricing: (pricing: Pricing) =>
    request<Pricing>('/api/v1/pricing', { method: 'PUT', body: JSON.stringify(pricing) }),

  // Secrets (per-project)
  setSecret: (projectId: string, key: string, value: string) =>
    request<{ key: string; masked: string; status: string }>(`/api/v1/projects/${projectId}/secrets/${key}`, {
      method: 'PUT', body: JSON.stringify({ value }),
    }),
  listSecrets: (projectId: string) =>
    request<SecretEntryResponse[]>(`/api/v1/projects/${projectId}/secrets`),

  // Connection testing
  testWarehouse: (projectId: string) =>
    request<TestConnectionResult>(`/api/v1/projects/${projectId}/test/warehouse`, { method: 'POST' }),
  testLLM: (projectId: string) =>
    request<TestConnectionResult>(`/api/v1/projects/${projectId}/test/llm`, { method: 'POST' }),
  testEmbedding: (projectId: string) =>
    request<TestConnectionResult>(`/api/v1/projects/${projectId}/test/embedding`, { method: 'POST' }),
  testBlurbLLM: (projectId: string) =>
    request<TestConnectionResult>(`/api/v1/projects/${projectId}/test/blurb-llm`, { method: 'POST' }),

  // Embedding providers
  listEmbeddingProviders: () => request<EmbeddingProviderMeta[]>('/api/v1/providers/embedding'),
  // Live-list parity with LLM: the dashboard's shared "Load models"
  // phase hits this endpoint with user-entered credentials. Response
  // merges shipped catalog + live-upstream rows — see
  // writeEmbeddingLiveModelsResponse on the API side.
  listLiveEmbeddingModels: (providerID: string, config: Record<string, string>) =>
    request<EmbeddingLiveModelsResponse>(
      `/api/v1/providers/embedding/${encodeURIComponent(providerID)}/models/live`,
      { method: 'POST', body: JSON.stringify({ config }) }
    ),
  listLiveEmbeddingModelsForProject: (projectID: string) =>
    request<EmbeddingLiveModelsResponse>(
      `/api/v1/projects/${encodeURIComponent(projectID)}/providers/embedding/models/live`,
      { method: 'POST', body: JSON.stringify({}) }
    ),

  // Vector search
  searchInsights: (projectId: string, req: SearchRequest) =>
    request<SearchResponse>(`/api/v1/projects/${projectId}/search`, { method: 'POST', body: JSON.stringify(req) }),
  crossProjectSearch: (req: CrossProjectSearchRequest) =>
    request<SearchResponse>('/api/v1/search', { method: 'POST', body: JSON.stringify(req) }),
  askInsights: (projectId: string, req: AskRequest) =>
    request<AskResponse>(`/api/v1/projects/${projectId}/ask`, { method: 'POST', body: JSON.stringify(req) }),
  // LLM-generated starter questions for an insight / recommendation. Enterprise
  // route; returns { questions: [] } (or 404 → caught by the caller) when the
  // feature is unavailable, so the UI simply renders no chips.
  getAskSuggestions: (projectId: string, body: { type: string; id: string }) =>
    request<AskSuggestionsResponse>(`/api/v1/projects/${projectId}/ask/suggestions`, { method: 'POST', body: JSON.stringify(body) }),

  // Standalone insights & recommendations (denormalized collections)
  listStandaloneInsights: (projectId: string, limit = 50, offset = 0) =>
    request<StandaloneInsight[]>(`/api/v1/projects/${projectId}/insights?limit=${limit}&offset=${offset}`),
  getStandaloneInsight: (projectId: string, insightId: string) =>
    request<StandaloneInsight>(`/api/v1/projects/${projectId}/insights/${insightId}`),
  listStandaloneRecommendations: (projectId: string, limit = 50, offset = 0) =>
    request<StandaloneRecommendation[]>(`/api/v1/projects/${projectId}/recommendations?limit=${limit}&offset=${offset}`),
  getStandaloneRecommendation: (projectId: string, recId: string) =>
    request<StandaloneRecommendation>(`/api/v1/projects/${projectId}/recommendations/${recId}`),

  // Search history
  listSearchHistory: (projectId: string, limit = 20) =>
    request<SearchHistoryEntry[]>(`/api/v1/projects/${projectId}/search/history?limit=${limit}`),

  // Ask sessions (conversations)
  listAskSessions: (projectId: string, limit = 20) =>
    request<AskSession[]>(`/api/v1/projects/${projectId}/ask/sessions?limit=${limit}`),
  getAskSession: (projectId: string, sessionId: string) =>
    request<AskSession>(`/api/v1/projects/${projectId}/ask/sessions/${sessionId}`),
  deleteAskSession: (projectId: string, sessionId: string) =>
    request<{ status: string }>(`/api/v1/projects/${projectId}/ask/sessions/${sessionId}`, { method: 'DELETE' }),

  // Bookmark lists
  listBookmarkLists: (projectId: string) =>
    request<BookmarkList[]>(`/api/v1/projects/${projectId}/lists`),
  createBookmarkList: (projectId: string, data: { name: string; description?: string; color?: string }) =>
    request<BookmarkList>(`/api/v1/projects/${projectId}/lists`, {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  getBookmarkList: (projectId: string, listId: string) =>
    request<BookmarkListWithItems>(`/api/v1/projects/${projectId}/lists/${listId}`),
  updateBookmarkList: (projectId: string, listId: string, data: Partial<Pick<BookmarkList, 'name' | 'description' | 'color'>>) =>
    request<BookmarkList>(`/api/v1/projects/${projectId}/lists/${listId}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
    }),
  deleteBookmarkList: (projectId: string, listId: string) =>
    request<{ status: string }>(`/api/v1/projects/${projectId}/lists/${listId}`, { method: 'DELETE' }),

  // Bookmarks within a list
  addBookmark: (projectId: string, listId: string, data: { discovery_id?: string; target_type: 'insight' | 'recommendation'; target_id: string; note?: string }) =>
    request<Bookmark>(`/api/v1/projects/${projectId}/lists/${listId}/items`, {
      method: 'POST',
      body: JSON.stringify(data),
    }),
  removeBookmark: (projectId: string, listId: string, bookmarkId: string) =>
    request<{ status: string }>(`/api/v1/projects/${projectId}/lists/${listId}/items/${bookmarkId}`, {
      method: 'DELETE',
    }),
  // Which of this user's lists contain a given target? Powers the
  // "Add to list" menu's checkmark state and the bookmark icon's fill state.
  listsContaining: (projectId: string, targetType: 'insight' | 'recommendation', targetId: string) =>
    request<string[]>(`/api/v1/projects/${projectId}/bookmarks?target_type=${encodeURIComponent(targetType)}&target_id=${encodeURIComponent(targetId)}`),

  // Read state
  markRead: (projectId: string, targetType: 'insight' | 'recommendation', targetId: string) =>
    request<{ target_id: string; read_at: string }>(`/api/v1/projects/${projectId}/reads`, {
      method: 'POST',
      body: JSON.stringify({ target_type: targetType, target_id: targetId }),
    }),
  markUnread: (projectId: string, targetType: 'insight' | 'recommendation', targetId: string) =>
    request<{ status: string }>(`/api/v1/projects/${projectId}/reads`, {
      method: 'DELETE',
      body: JSON.stringify({ target_type: targetType, target_id: targetId }),
    }),
  listReadIDs: (projectId: string, targetType: 'insight' | 'recommendation') =>
    request<string[]>(`/api/v1/projects/${projectId}/reads?target_type=${encodeURIComponent(targetType)}`),
};
// build trigger 20260319111744
// coverage trigger


