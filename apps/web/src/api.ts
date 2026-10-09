export interface SessionUser {
  id: string;
  organizationId: string;
  email: string;
  displayName: string;
  role: string;
}

export interface Project {
  id: string;
  name: string;
  status: string;
}

export type FolderEditor = 'explorer' | 'vscode';

export interface ProjectFolderStatus {
  available: boolean;
}

export interface Employee {
  id: string;
  name: string;
  role: string;
  department: string;
  status: string;
}

export interface TaskReferenceImage {
  id: string;
  fileName: string;
  contentType: string;
  sizeBytes: number;
}

export interface Task {
  id: string;
  projectId: string;
  assigneeEmployeeId: string;
  title: string;
  description: string;
  acceptanceCriteria: string[];
  requiredChecks: string[];
  referenceImages?: TaskReferenceImage[];
  manifestDigest?: string;
  taskType: string;
  priority: string;
  status: string;
  version: number;
  createdAt: string;
}

export interface Repository {
  id: string;
  projectId: string;
  provider: string;
  owner: string;
  name: string;
  defaultBranch: string;
}

export interface ProviderInfo {
  key: string;
  displayName: string;
  kind: string;
  adapterStatus: string;
  needsBaseUrl: boolean;
  needsCredential: boolean;
  configured: boolean;
  enabled: boolean;
  label: string;
  baseUrl?: string;
  credentialEnv?: string;
}

export interface SaveProviderInput {
  label: string;
  baseUrl: string;
  credentialEnv: string;
  enabled: boolean;
}

export interface PullRequestInfo {
  id: string;
  number: number;
  url: string;
  branchName: string;
  headSha: string;
  baseSha: string;
  state: string;
  mergedAt: string | null;
}

export interface CreateTaskInput {
  assigneeEmployeeId: string;
  title: string;
  description: string;
  acceptanceCriteria: string[];
  requiredChecks: string[];
  taskType: string;
  priority: string;
}

export interface ProjectEvent {
  eventId: string;
  schemaVersion: string;
  eventType: string;
  organizationId: string;
  projectId: string;
  taskId: string | null;
  employeeId: string | null;
  attemptId: string | null;
  sessionId: string | null;
  workspaceId: string | null;
  streamSequence: number;
  occurredAt: string;
  recordedAt: string;
  producer: string;
  actor: { type: string; id: string };
  correlationId: string;
  causationId: string | null;
  data: Record<string, unknown>;
}

export interface EventPage {
  items: ProjectEvent[];
  nextCursor: string;
  hasMore: boolean;
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    readonly missing?: string[],
  ) {
    super(code);
    this.name = 'ApiError';
  }
}

export function readCookieValue(name: string, cookieString: string): string | undefined {
  for (const part of cookieString.split(';')) {
    const separator = part.indexOf('=');
    if (separator < 0 || part.slice(0, separator).trim() !== name) continue;
    const value = part.slice(separator + 1).trim();
    try {
      return decodeURIComponent(value);
    } catch {
      return value;
    }
  }
  return undefined;
}

export class ApiClient {
  private readonly baseURL: string;
  private developmentSessionBootstrap: Promise<SessionUser> | null = null;

  constructor(
    baseURL: string,
    private readonly fetcher: typeof fetch = fetch,
    private readonly cookieReader: () => string = () => document.cookie,
  ) {
    this.baseURL = baseURL.replace(/\/+$/, '');
  }

  async login(input: { organizationId: string; email: string; password: string }): Promise<SessionUser> {
    const response = await this.request<{ user: SessionUser }>('/api/v1/auth/login', {
      method: 'POST',
      body: JSON.stringify(input),
    });
    return response.user;
  }

  async createDevelopmentSession(): Promise<SessionUser> {
    const response = await this.request<{ user: SessionUser }>('/api/v1/auth/dev-session', {
      method: 'POST',
      body: JSON.stringify({}),
    });
    return response.user;
  }

  getSession(allowDevelopmentBypass = false): Promise<SessionUser> {
    if (allowDevelopmentBypass && this.developmentSessionBootstrap) {
      return this.developmentSessionBootstrap;
    }

    const sessionRequest = this.request<{ user: SessionUser }>('/api/v1/auth/session')
      .then((response) => response.user);
    if (!allowDevelopmentBypass) return sessionRequest;

    const bootstrap = sessionRequest.catch((error: unknown) => {
      if (error instanceof ApiError && error.status === 401) {
        return this.createDevelopmentSession();
      }
      throw error;
    }).finally(() => {
      if (this.developmentSessionBootstrap === bootstrap) {
        this.developmentSessionBootstrap = null;
      }
    });
    this.developmentSessionBootstrap = bootstrap;
    return bootstrap;
  }

  async logout(): Promise<void> {
    await this.request('/api/v1/auth/logout', { method: 'POST', csrf: true });
  }

  async listProjects(): Promise<Project[]> {
    const response = await this.request<{ items: Project[] }>('/api/v1/projects');
    return response.items;
  }

  getProjectFolderStatus(projectID: string): Promise<ProjectFolderStatus> {
    return this.request<ProjectFolderStatus>(`/api/v1/projects/${encodeURIComponent(projectID)}/folder`);
  }

  async openProjectFolder(projectID: string, editor: FolderEditor): Promise<void> {
    await this.request(`/api/v1/projects/${encodeURIComponent(projectID)}/folder/open`, {
      method: 'POST',
      body: JSON.stringify({ editor }),
      csrf: true,
    });
  }

  async listEmployees(): Promise<Employee[]> {
    const response = await this.request<{ items: Employee[] }>('/api/v1/employees');
    return response.items;
  }

  async listProviders(): Promise<ProviderInfo[]> {
    return this.request<ProviderInfo[]>('/api/v1/providers');
  }

  saveProvider(providerKey: string, input: SaveProviderInput): Promise<ProviderInfo> {
    return this.request<ProviderInfo>(
      `/api/v1/providers/${encodeURIComponent(providerKey)}`,
      { method: 'PUT', body: JSON.stringify(input), csrf: true },
    );
  }

  async getProjectRepository(projectID: string): Promise<Repository | null> {
    try {
      return await this.request<Repository>(
        `/api/v1/projects/${encodeURIComponent(projectID)}/repository`,
      );
    } catch (error) {
      if (error instanceof ApiError && error.code === 'repository_not_found') return null;
      throw error;
    }
  }

  saveProjectRepository(
    projectID: string,
    input: { owner: string; name: string; defaultBranch: string },
  ): Promise<Repository> {
    return this.request<Repository>(
      `/api/v1/projects/${encodeURIComponent(projectID)}/repository`,
      { method: 'PUT', body: JSON.stringify(input), csrf: true },
    );
  }

  async listTasks(projectID: string): Promise<Task[]> {
    const response = await this.request<{ items: Task[] }>(
      `/api/v1/projects/${encodeURIComponent(projectID)}/tasks`,
    );
    return response.items;
  }

  listEvents(projectID: string, after?: string, signal?: AbortSignal): Promise<EventPage> {
    const query = after === undefined ? '' : `?after=${encodeURIComponent(after)}&limit=100`;
    return this.request<EventPage>(`/api/v1/projects/${encodeURIComponent(projectID)}/events${query}`, { signal });
  }

  eventStreamURL(projectID: string, after: string): string {
    return `${this.baseURL}/api/v1/projects/${encodeURIComponent(projectID)}/events/stream?after=${encodeURIComponent(after)}`;
  }

  createTask(
    projectID: string,
    input: CreateTaskInput,
    idempotencyKey: string,
    referenceImages: File[] = [],
  ): Promise<Task> {
    let body: BodyInit;
    if (referenceImages.length > 0) {
      const form = new FormData();
      form.append('task', JSON.stringify(input));
      referenceImages.forEach((image) => form.append('referenceImages', image, image.name));
      body = form;
    } else {
      body = JSON.stringify(input);
    }
    return this.request<Task>(`/api/v1/projects/${encodeURIComponent(projectID)}/tasks`, {
      method: 'POST',
      headers: { 'Idempotency-Key': idempotencyKey },
      body,
      csrf: true,
    });
  }

  saveTaskToBacklog(projectID: string, taskID: string, expectedVersion: number, idempotencyKey: string): Promise<Task> {
    return this.request<Task>(
      `/api/v1/projects/${encodeURIComponent(projectID)}/tasks/${encodeURIComponent(taskID)}/backlog`,
      {
        method: 'POST',
        headers: { 'Idempotency-Key': idempotencyKey },
        body: JSON.stringify({ expectedVersion }),
        csrf: true,
      },
    );
  }

  markTaskReady(projectID: string, taskID: string, expectedVersion: number, idempotencyKey: string, manifestDigest?: string): Promise<Task> {
    return this.request<Task>(
      `/api/v1/projects/${encodeURIComponent(projectID)}/tasks/${encodeURIComponent(taskID)}/ready`,
      {
        method: 'POST',
        headers: { 'Idempotency-Key': idempotencyKey },
        body: JSON.stringify(manifestDigest ? { expectedVersion, manifestDigest } : { expectedVersion }),
        csrf: true,
      },
    );
  }

  startTask(projectID: string, taskID: string, expectedVersion: number, idempotencyKey: string): Promise<Task> {
    return this.request<Task>(
      `/api/v1/projects/${encodeURIComponent(projectID)}/tasks/${encodeURIComponent(taskID)}/start`,
      {
        method: 'POST',
        headers: { 'Idempotency-Key': idempotencyKey },
        body: JSON.stringify({ expectedVersion }),
        csrf: true,
      },
    );
  }

  retryTask(projectID: string, taskID: string, expectedVersion: number, idempotencyKey: string): Promise<Task> {
    return this.request<Task>(
      `/api/v1/projects/${encodeURIComponent(projectID)}/tasks/${encodeURIComponent(taskID)}/retry`,
      {
        method: 'POST',
        headers: { 'Idempotency-Key': idempotencyKey },
        body: JSON.stringify({ expectedVersion }),
        csrf: true,
      },
    );
  }

  cancelTask(projectID: string, taskID: string, expectedVersion: number, idempotencyKey: string, reason?: string): Promise<Task> {
    return this.request<Task>(
      `/api/v1/projects/${encodeURIComponent(projectID)}/tasks/${encodeURIComponent(taskID)}/cancel`,
      {
        method: 'POST',
        headers: { 'Idempotency-Key': idempotencyKey },
        body: JSON.stringify(reason?.trim() ? { expectedVersion, reason } : { expectedVersion }),
        csrf: true,
      },
    );
  }

  getTaskPullRequest(projectID: string, taskID: string): Promise<PullRequestInfo> {
    return this.request<PullRequestInfo>(
      `/api/v1/projects/${encodeURIComponent(projectID)}/tasks/${encodeURIComponent(taskID)}/pull-request`,
    );
  }

  approveTask(projectID: string, taskID: string, input: { expectedVersion: number; headSha: string; reason?: string }, idempotencyKey: string): Promise<Task> {
    return this.request<Task>(
      `/api/v1/projects/${encodeURIComponent(projectID)}/tasks/${encodeURIComponent(taskID)}/approve`,
      {
        method: 'POST',
        headers: { 'Idempotency-Key': idempotencyKey },
        body: JSON.stringify(input.reason?.trim()
          ? { expectedVersion: input.expectedVersion, headSha: input.headSha, reason: input.reason }
          : { expectedVersion: input.expectedVersion, headSha: input.headSha }),
        csrf: true,
      },
    );
  }

  requestTaskChanges(projectID: string, taskID: string, input: { expectedVersion: number; reason: string }, idempotencyKey: string): Promise<Task> {
    return this.request<Task>(
      `/api/v1/projects/${encodeURIComponent(projectID)}/tasks/${encodeURIComponent(taskID)}/request-changes`,
      {
        method: 'POST',
        headers: { 'Idempotency-Key': idempotencyKey },
        body: JSON.stringify({ expectedVersion: input.expectedVersion, reason: input.reason }),
        csrf: true,
      },
    );
  }

  mergeTaskPullRequest(projectID: string, taskID: string, input: { expectedVersion: number; reason?: string }, idempotencyKey: string): Promise<Task> {
    return this.request<Task>(
      `/api/v1/projects/${encodeURIComponent(projectID)}/tasks/${encodeURIComponent(taskID)}/merge`,
      {
        method: 'POST',
        headers: { 'Idempotency-Key': idempotencyKey },
        body: JSON.stringify(input.reason?.trim()
          ? { expectedVersion: input.expectedVersion, reason: input.reason }
          : { expectedVersion: input.expectedVersion }),
        csrf: true,
      },
    );
  }

  referenceImageURL(projectID: string, taskID: string, imageID: string): string {
    return `${this.baseURL}/api/v1/projects/${encodeURIComponent(projectID)}/tasks/${encodeURIComponent(taskID)}/reference-images/${encodeURIComponent(imageID)}`;
  }

  private async request<T>(
    path: string,
    options: RequestInit & { csrf?: boolean } = {},
  ): Promise<T> {
    const { csrf = false, headers: inputHeaders, ...init } = options;
    const headers = new Headers(inputHeaders);
    const isFormData = typeof FormData !== 'undefined' && init.body instanceof FormData;
    if (init.body && !isFormData && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json');
    if (csrf) {
      const token = readCookieValue('dioffice_csrf', this.cookieReader());
      if (!token) throw new ApiError(403, 'csrf_token_missing');
      headers.set('X-CSRF-Token', token);
    }

    let response: Response;
    try {
      response = await this.fetcher.call(globalThis, `${this.baseURL}${path}`, {
        ...init,
        headers,
        credentials: 'include',
        cache: 'no-store',
      });
    } catch {
      throw new ApiError(0, 'network_error');
    }

    let payload: unknown;
    if (response.status !== 204) {
      try {
        payload = await response.json();
      } catch {
        payload = undefined;
      }
    }
    if (!response.ok) {
      const code =
        typeof payload === 'object' && payload !== null && 'error' in payload && typeof payload.error === 'string'
          ? payload.error
          : 'request_failed';
      const missing =
        typeof payload === 'object' && payload !== null && 'missing' in payload && Array.isArray(payload.missing)
          ? payload.missing.filter((field): field is string => typeof field === 'string')
          : undefined;
      throw new ApiError(response.status, code, missing);
    }
    return payload as T;
  }
}
