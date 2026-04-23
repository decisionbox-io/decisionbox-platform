// All API calls use relative paths (/api/v1/...) — Next.js rewrites
// proxy them to the backend API server-side. No direct browser-to-API calls.
const API_BASE = '';

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
    throw new Error(
      `Cannot connect to DecisionBox API at ${API_BASE}. ` +
      `Make sure the API is running (make dev-api or docker compose up). ` +
      `Original error: ${(err as Error).message}`
    );
  }

  const json = await res.json();

  if (!res.ok) {
    throw new Error(json.error || `API error: ${res.status}`);
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
  schedule: ScheduleConfig;
  profile: Record<string, unknown>;
  status: string;
  last_run_at: string | null;
  last_run_status: string;
  created_at: string;
  updated_at: string;
}

export interface EmbeddingConfig {
  provider: string;
  model: string;
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

export interface ScheduleConfig {
  enabled: boolean;
  cron_expr: string;
  max_steps: number;
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
  exploration_log?: ExplorationStep[];
  analysis_log?: AnalysisLogStep[];
  validation_log?: ValidationLogEntry[];
  created_at: string;
}

export interface Insight {
  id: string;
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
  discovered_at: string;
}

export interface InsightValidation {
  status: string;
  verified_count: number;
  original_count: number;
  reasoning: string;
}

export interface Recommendation {
  id: string;
  category: string;
  title: string;
  description: string;
  priority: number;
  target_segment: string;
  segment_size: number;
  expected_impact: { metric: string; estimated_improvement: string; reasoning: string };
  actions: string[];
  related_insight_ids?: string[];
  confidence: number;
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
}

export interface AnalysisLogStep {
  area_id: string;
  area_name: string;
  run_at: string;
  relevant_queries: number;
  tokens_in: number;
  tokens_out: number;
  duration_ms: number;
  insight_count: number;
  error: string;
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
  config_fields: ConfigField[];
  auth_methods?: AuthMethod[];
  models?: ModelInfo[];
}

export interface ModelInfo {
  id: string;
  display_name: string;
  wire: string; // "anthropic" | "openai-compat" | "google-native" | "" (unknown)
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
  steps: RunStep[];
  total_queries: number;
  successful_queries: number;
  failed_queries: number;
  insights_found: number;
}

export interface RunStep {
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
  insight_name: string;
  insight_severity: string;
  error: string;
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
  type: 'insight' | 'recommendation';
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
}

export interface SearchResponse {
  results: SearchResultItem[];
  embedding_model: string;
  projects_searched?: number;
  projects_excluded?: number;
}

export interface AskRequest {
  question: string;
  limit?: number;
  session_id?: string;
}

export interface AskResponse {
  answer: string;
  sources: SearchResultItem[];
  model: string;
  session_id: string;
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
  tokens_used: number;
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
  tokens_used?: number;
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

// --- API Functions ---

export const api = {
  // Providers (dynamic — registered in Go via init())
  listLLMProviders: () => request<ProviderMeta[]>('/api/v1/providers/llm'),
  listLiveLLMModels: (providerID: string, config: Record<string, string>) =>
    request<LiveModelsResponse>(`/api/v1/providers/llm/${encodeURIComponent(providerID)}/models/live`, {
      method: 'POST',
      body: JSON.stringify({ config }),
    }),
  listLiveLLMModelsForProject: (projectID: string) =>
    request<LiveModelsResponse>(`/api/v1/projects/${encodeURIComponent(projectID)}/providers/llm/models/live`, {
      method: 'POST',
      body: JSON.stringify({}),
    }),
  listWarehouseProviders: () => request<ProviderMeta[]>('/api/v1/providers/warehouse'),

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
  deleteProject: (id: string) =>
    request<{ deleted: string }>(`/api/v1/projects/${id}`, { method: 'DELETE' }),

  // Prompts
  getPrompts: (projectId: string) =>
    request<ProjectPrompts>(`/api/v1/projects/${projectId}/prompts`),
  updatePrompts: (projectId: string, prompts: ProjectPrompts) =>
    request<ProjectPrompts>(`/api/v1/projects/${projectId}/prompts`, { method: 'PUT', body: JSON.stringify(prompts) }),

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

  // Embedding providers
  listEmbeddingProviders: () => request<EmbeddingProviderMeta[]>('/api/v1/providers/embedding'),

  // Vector search
  searchInsights: (projectId: string, req: SearchRequest) =>
    request<SearchResponse>(`/api/v1/projects/${projectId}/search`, { method: 'POST', body: JSON.stringify(req) }),
  crossProjectSearch: (req: CrossProjectSearchRequest) =>
    request<SearchResponse>('/api/v1/search', { method: 'POST', body: JSON.stringify(req) }),
  askInsights: (projectId: string, req: AskRequest) =>
    request<AskResponse>(`/api/v1/projects/${projectId}/ask`, { method: 'POST', body: JSON.stringify(req) }),

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


