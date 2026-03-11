const API_BASE = process.env.NEXT_PUBLIC_API_URL || 'http://localhost:8080';

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
}

export interface LLMConfig {
  provider: string;
  model: string;
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
  discovery_date: string;
  total_steps: number;
  duration: number;
  insights: Insight[];
  recommendations: Recommendation[];
  summary: Summary;
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
  confidence: number;
}

export interface Summary {
  text: string;
  key_findings: string[];
  top_recommendations: string[];
  total_insights: number;
  total_recommendations: number;
  queries_executed: number;
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
  triggerDiscovery: (projectId: string, areas?: string[]) =>
    request<{ status: string; message: string; run_id?: string }>(`/api/v1/projects/${projectId}/discover`, {
      method: 'POST',
      body: areas && areas.length > 0 ? JSON.stringify({ areas }) : undefined,
    }),
  getRun: (runId: string) =>
    request<DiscoveryRunStatus>(`/api/v1/runs/${runId}`),
  cancelRun: (runId: string) =>
    request<{ status: string; message: string }>(`/api/v1/runs/${runId}`, { method: 'DELETE' }),
  listDiscoveries: (projectId: string) =>
    request<DiscoveryResult[]>(`/api/v1/projects/${projectId}/discoveries`),
  getLatestDiscovery: (projectId: string) =>
    request<DiscoveryResult>(`/api/v1/projects/${projectId}/discoveries/latest`),
  getDiscoveryByDate: (projectId: string, date: string) =>
    request<DiscoveryResult>(`/api/v1/projects/${projectId}/discoveries/${date}`),
  getProjectStatus: (projectId: string) =>
    request<ProjectStatus>(`/api/v1/projects/${projectId}/status`),
};
