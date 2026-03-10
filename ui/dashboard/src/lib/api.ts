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
  dataset: string;
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
  status: string;
  last_run_at: string | null;
  last_run_status: string;
  last_discovery_date?: string;
  last_insights_count?: number;
}

// --- API Functions ---

export const api = {
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

  // Discovery
  triggerDiscovery: (projectId: string) =>
    request<{ status: string; message: string }>(`/api/v1/projects/${projectId}/discover`, { method: 'POST' }),
  listDiscoveries: (projectId: string) =>
    request<DiscoveryResult[]>(`/api/v1/projects/${projectId}/discoveries`),
  getLatestDiscovery: (projectId: string) =>
    request<DiscoveryResult>(`/api/v1/projects/${projectId}/discoveries/latest`),
  getDiscoveryByDate: (projectId: string, date: string) =>
    request<DiscoveryResult>(`/api/v1/projects/${projectId}/discoveries/${date}`),
  getProjectStatus: (projectId: string) =>
    request<ProjectStatus>(`/api/v1/projects/${projectId}/status`),
};
