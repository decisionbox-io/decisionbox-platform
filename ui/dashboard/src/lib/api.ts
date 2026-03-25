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

export interface Project {
  id: string;
  name: string;
  description: string;
  domain: string;
  category: string;
  warehouse: WarehouseConfig;
  llm: LLMConfig;
  schedule: ScheduleConfig;
  profile: Record<string, unknown>;
  status: string;
  last_run_at: string | null;
  last_run_status: string;
  created_at: string;
  updated_at: string;
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

// --- API Functions ---

export const api = {
  // Providers (dynamic — registered in Go via init())
  listLLMProviders: () => request<ProviderMeta[]>('/api/v1/providers/llm'),
  listWarehouseProviders: () => request<ProviderMeta[]>('/api/v1/providers/warehouse'),

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
  triggerDiscovery: (projectId: string, options?: { areas?: string[]; max_steps?: number }) =>
    request<{ status: string; message: string; run_id?: string }>(`/api/v1/projects/${projectId}/discover`, {
      method: 'POST',
      body: options ? JSON.stringify(options) : undefined,
    }),
  getRun: (runId: string) =>
    request<DiscoveryRunStatus>(`/api/v1/runs/${runId}`),
  cancelRun: (runId: string) =>
    request<{ status: string; message: string }>(`/api/v1/runs/${runId}`, { method: 'DELETE' }),
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
};
// build trigger 20260319111744
// coverage trigger


