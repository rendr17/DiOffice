import { useEffect, useRef, useState, type FormEvent } from 'react';
import { ApiClient, ApiError, type Employee, type FolderEditor, type Project, type ProviderInfo, type PullRequestInfo, type Repository, type SaveProviderInput, type SessionUser, type Task } from './api';
import { commandToTaskFields } from './task-command';
import { AttachmentPreview, CancelTaskButton, EmployeeSnapshotWarning, EventConnectionBadge, InspectorDialog, ManifestDigestField, MarkReadyButton, OfficeScene, PixelIcon, PixelSprite, ProjectFolderActions, ProvidersPanel, PullRequestReview, RepositoryConnection, RetryTaskButton, SaveDraftToBacklogButton, StartTaskButton, StateBadge, WorkstationContent, type ProjectFolderAvailability } from './office-ui';
import { StudioHUD, type StudioPane } from './studio-hud';
import { getStudioArea, initialStudioAvatar, STUDIO_EMPLOYEE_X, STUDIO_WORLD_WIDTH } from './studio-navigation';
import { useProjectActivity } from './use-project-activity';

type SessionState = 'checking' | 'signed-out' | 'signed-in';
type TaskFormState = {
  instruction: string;
  assigneeEmployeeId: string;
  priority: string;
  referenceImages: File[];
};

const defaultApiBaseURL = import.meta.env?.DEV ? '' : 'http://127.0.0.1:8080';
const api = new ApiClient(import.meta.env?.VITE_API_BASE_URL || defaultApiBaseURL);
const devAuthBypass = import.meta.env?.DEV === true && import.meta.env?.VITE_DEV_AUTH_BYPASS === 'true';
const maxReferenceImageCount = 5;
const maxReferenceImageBytes = 8 * 1024 * 1024;
const maxReferenceImageTotalBytes = maxReferenceImageCount * maxReferenceImageBytes;

async function fetchDirectory() {
  const [projects, employees, providers] = await Promise.all([api.listProjects(), api.listEmployees(), api.listProviders()]);
  return { projects, employees, providers };
}

function newIdempotencyKey() {
  return globalThis.crypto?.randomUUID?.() ?? `task-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

function messageFor(error: unknown) {
  if (!(error instanceof ApiError)) return 'Something went wrong. Please try again.';
  switch (error.code) {
    case 'network_error':
      return 'Cannot reach the API. Check that the local API is running, then retry.';
    case 'service_unavailable':
      return 'The API or database is temporarily unavailable. Retry in a moment.';
    case 'invalid_credentials':
      return 'The organization ID, email, or password is incorrect.';
    case 'unauthorized':
      return 'Your session has expired. Sign in again to continue.';
    case 'csrf_validation_failed':
    case 'csrf_token_missing':
      return 'The security token is missing or expired. Refresh the page and retry.';
    case 'origin_not_allowed':
      return 'This web origin is not allowed by the API configuration.';
    case 'invalid_task':
      return 'Check the task details and try again.';
    case 'invalid_reference_image':
      return 'Attach up to 5 valid PNG or JPEG images, no larger than 8 MiB each.';
    case 'invalid_task_request':
      return 'The task request could not be read. Review the instruction and attachments, then retry.';
    case 'reference_image_storage_unavailable':
      return 'Private image storage is unavailable. Check the API storage configuration, then retry.';
    case 'project_not_found':
      return 'This project is no longer available. Choose another project.';
    case 'task_not_found':
      return 'This task is no longer available in the selected project. Refresh the task board.';
    case 'stale_task_version':
      return 'This task changed in another window. Refresh the task board before trying again.';
    case 'invalid_state_transition':
      return 'This task cannot move to the requested state. Refresh the board and check its status.';
    case 'task_incomplete':
      return '';
    case 'active_attempt_exists':
      return 'Employee ini masih memiliki attempt aktif. Tunggu selesai atau batalkan task yang berjalan.';
    case 'invalid_repository':
      return 'Detail repository tidak valid. Periksa owner, nama repo, dan default branch.';
    case 'repository_not_found':
      return 'Repository belum terhubung ke project ini.';
    case 'idempotency_key_conflict':
      return 'This task request changed during a retry. Edit the task and submit again.';
    case 'folder_not_configured':
      return 'Folder project belum terdaftar pada local gateway allowlist.';
    case 'folder_bridge_unavailable':
      return 'Local folder bridge tidak tersedia. Jalankan gateway lokal lalu coba lagi.';
    case 'folder_open_failed':
      return 'Folder tidak bisa dibuka. Pastikan File Explorer atau VS Code CLI tersedia.';
    default:
      return error.status >= 500
        ? 'The server could not complete this request. Try again shortly.'
        : 'The request could not be completed. Check the details and retry.';
  }
}

const missingRequirementLabels: Record<string, string> = {
  description: 'deskripsi task',
  acceptanceCriteria: 'acceptance criteria',
  manifestDigest: 'digest manifest .dioffice/execution.json',
  repository: 'repository GitHub terhubung',
};

function describeTaskCommandError(error: unknown) {
  if (error instanceof ApiError && error.code === 'task_incomplete') {
    const missing = (error.missing ?? []).map((field) => missingRequirementLabels[field] ?? field);
    return missing.length > 0
      ? `Task belum READY — lengkapi dulu: ${missing.join(', ')}.`
      : 'Task belum memenuhi syarat READY.';
  }
  return messageFor(error);
}

function formatDate(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? ''
    : new Intl.DateTimeFormat('id-ID', { dateStyle: 'medium', timeStyle: 'short' }).format(date);
}

export function App() {
  const [sessionState, setSessionState] = useState<SessionState>('checking');
  const [user, setUser] = useState<SessionUser | null>(null);
  const [loginForm, setLoginForm] = useState({ organizationId: '', email: '', password: '' });
  const [projects, setProjects] = useState<Project[]>([]);
  const [employees, setEmployees] = useState<Employee[]>([]);
  const [selectedProjectId, setSelectedProjectId] = useState('');
  const [projectFolderStatus, setProjectFolderStatus] = useState<ProjectFolderAvailability>('checking');
  const [projectFolderRevision, setProjectFolderRevision] = useState(0);
  const [folderPendingEditor, setFolderPendingEditor] = useState<FolderEditor | null>(null);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [taskForm, setTaskForm] = useState<TaskFormState>({
    instruction: '', assigneeEmployeeId: '', priority: 'NORMAL', referenceImages: [],
  });
  const [loginPending, setLoginPending] = useState(false);
  const [logoutPending, setLogoutPending] = useState(false);
  const [taskPending, setTaskPending] = useState(false);
  const [taskCommandPending, setTaskCommandPending] = useState(false);
  const [repository, setRepository] = useState<Repository | null>(null);
  const [repositoryPending, setRepositoryPending] = useState(false);
  const [manifestDigestInput, setManifestDigestInput] = useState('');
  const [workspaceLoading, setWorkspaceLoading] = useState(false);
  const [workspaceLoadError, setWorkspaceLoadError] = useState(false);
  const [employeeSnapshotError, setEmployeeSnapshotError] = useState('');
  const [tasksLoading, setTasksLoading] = useState(false);
  const [taskLoadError, setTaskLoadError] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [taskRevision, setTaskRevision] = useState(0);
  const [employeeRevision, setEmployeeRevision] = useState(0);
  const [view, setView] = useState<StudioPane>('office');
  const [ownerWorldX, setOwnerWorldX] = useState(initialStudioAvatar().x);
  const [taskFilter, setTaskFilter] = useState('ALL');
  const [inspectedEmployee, setInspectedEmployee] = useState<Employee | null>(null);
  const [inspectedTask, setInspectedTask] = useState<Task | null>(null);
  const [inspectedTaskPR, setInspectedTaskPR] = useState<PullRequestInfo | null>(null);
  const [prLoading, setPRLoading] = useState(false);
  const [providers, setProviders] = useState<ProviderInfo[]>([]);
  const [providerPending, setProviderPending] = useState<string | null>(null);
  const idempotencyKey = useRef(newIdempotencyKey());
  const taskCommandKeys = useRef(new Map<string, string>());
  const referenceImagesInput = useRef<HTMLInputElement>(null);
  const instructionInput = useRef<HTMLTextAreaElement>(null);
  const taskSnapshotScope = useRef('');

  const applyDirectory = (directory: { projects: Project[]; employees: Employee[]; providers?: ProviderInfo[] }) => {
    setProjects(directory.projects);
    setEmployees(directory.employees);
    if (directory.providers) setProviders(directory.providers);
    setEmployeeSnapshotError('');
    setSelectedProjectId((current) =>
      directory.projects.some((project) => project.id === current)
        ? current
        : directory.projects[0]?.id ?? '',
    );
    setTaskForm((current) => ({
      ...current,
      assigneeEmployeeId: directory.employees.some((employee) => employee.id === current.assigneeEmployeeId)
        ? current.assigneeEmployeeId
        : directory.employees[0]?.id ?? '',
    }));
  };

  const loadWorkspaceDirectory = async (isActive: () => boolean = () => true) => {
    setWorkspaceLoading(true);
    setWorkspaceLoadError(false);
    try {
      const directory = await fetchDirectory();
      if (isActive()) applyDirectory(directory);
    } catch (loadError) {
      if (isActive()) {
        setWorkspaceLoadError(true);
        setError(messageFor(loadError));
      }
    } finally {
      if (isActive()) setWorkspaceLoading(false);
    }
  };

  useEffect(() => {
    let active = true;
    void api.getSession(devAuthBypass).then(async (sessionUser) => {
      if (!active) return;
      setUser(sessionUser);
      setSessionState('signed-in');
      await loadWorkspaceDirectory(() => active);
    }).catch((sessionError: unknown) => {
      if (!active) return;
      setSessionState('signed-out');
      if (!(sessionError instanceof ApiError && sessionError.status === 401)) {
        setError(messageFor(sessionError));
      }
    });
    return () => { active = false; };
  }, []);

  useEffect(() => {
    if (!user || !selectedProjectId) {
      setTasks([]);
      return;
    }
    let active = true;
    setTasksLoading(true);
    setTaskLoadError(false);
    const snapshotScope = `${user.id}:${selectedProjectId}`;
    if (taskSnapshotScope.current !== snapshotScope) setTasks([]);
    setError('');
    void api.listTasks(selectedProjectId).then((items) => {
      if (active) {
        taskSnapshotScope.current = snapshotScope;
        setTasks(items);
        setInspectedTask((current) => current ? items.find((task) => task.id === current.id) ?? null : null);
      }
    }).catch((loadError: unknown) => {
      if (active) {
        setTasks([]);
        setTaskLoadError(true);
        setError(messageFor(loadError));
      }
    }).finally(() => {
      if (active) setTasksLoading(false);
    });
    return () => { active = false; };
  }, [user, selectedProjectId, taskRevision]);

  useEffect(() => {
    if (!user || !selectedProjectId) {
      setRepository(null);
      return;
    }
    let active = true;
    void api.getProjectRepository(selectedProjectId).then((connected) => {
      if (active) setRepository(connected);
    }).catch(() => {
      if (active) setRepository(null);
    });
    return () => { active = false; };
  }, [user?.id, selectedProjectId]);

  useEffect(() => {
    if (!user || !selectedProjectId) {
      setProjectFolderStatus('unavailable');
      return;
    }
    let active = true;
    setProjectFolderStatus('checking');
    void api.getProjectFolderStatus(selectedProjectId).then(({ available }) => {
      if (active) setProjectFolderStatus(available ? 'available' : 'unavailable');
    }).catch(() => {
      if (active) setProjectFolderStatus('error');
    });
    return () => { active = false; };
  }, [user?.id, selectedProjectId, projectFolderRevision]);

  useEffect(() => {
    setInspectedTaskPR(null);
    const task = inspectedTask;
    if (!task || (task.status !== 'IN_REVIEW' && task.status !== 'DONE')) {
      setPRLoading(false);
      return;
    }
    let active = true;
    setPRLoading(true);
    void api.getTaskPullRequest(task.projectId, task.id).then((pr) => {
      if (active) setInspectedTaskPR(pr);
    }).catch(() => {
      if (active) setInspectedTaskPR(null);
    }).finally(() => {
      if (active) setPRLoading(false);
    });
    return () => { active = false; };
  }, [inspectedTask?.id, inspectedTask?.status]);

  useEffect(() => {
    if (!user || employeeRevision === 0) return;
    let active = true;
    void api.listEmployees().then((items) => {
      if (!active) return;
      setEmployees(items);
      setInspectedEmployee((current) => current ? items.find((employee) => employee.id === current.id) ?? null : null);
      setEmployeeSnapshotError('');
    }).catch((loadError: unknown) => {
      if (!active) return;
      if (loadError instanceof ApiError && loadError.status === 401) {
        clearSession();
        setError(messageFor(loadError));
      } else {
        // A transient refresh failure cannot revoke a loaded project scope or
        // discard durable Activity. Keep the last snapshot, explicitly stale.
        setEmployeeSnapshotError(messageFor(loadError));
      }
    });
    return () => { active = false; };
  }, [user, employeeRevision]);

  const handleLogin = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setLoginPending(true);
    setError('');
    try {
      const signedInUser = await api.login(loginForm);
      setUser(signedInUser);
      setSessionState('signed-in');
      setLoginForm((current) => ({ ...current, password: '' }));
      await loadWorkspaceDirectory();
    } catch (loginError) {
      setError(messageFor(loginError));
    } finally {
      setLoginPending(false);
    }
  };

  const handleDevelopmentSession = async () => {
    setLoginPending(true);
    setError('');
    try {
      const developmentUser = await api.createDevelopmentSession();
      setUser(developmentUser);
      setSessionState('signed-in');
      await loadWorkspaceDirectory();
    } catch (sessionError) {
      setError(messageFor(sessionError));
    } finally {
      setLoginPending(false);
    }
  };

  const clearSession = () => {
    setUser(null);
    setSessionState('signed-out');
    setProjects([]);
    setEmployees([]);
    setTasks([]);
    setSelectedProjectId('');
    setProjectFolderStatus('unavailable');
    setFolderPendingEditor(null);
    setWorkspaceLoadError(false);
    setEmployeeSnapshotError('');
    setEmployeeRevision(0);
    setTaskLoadError(false);
    setInspectedEmployee(null);
    setInspectedTask(null);
    setRepository(null);
    setManifestDigestInput('');
    setView('office');
    setTaskForm((current) => ({ ...current, instruction: '', referenceImages: [] }));
    if (referenceImagesInput.current) referenceImagesInput.current.value = '';
    setLoginForm((current) => ({ ...current, password: '' }));
  };

  const { activity, retry: retryActivity } = useProjectActivity(api,
    user && selectedProjectId && !workspaceLoadError ? { organizationId: user.organizationId, projectId: selectedProjectId } : null, {
      onInvalidate: () => {
        setTaskRevision((current) => current + 1);
        setEmployeeRevision((current) => current + 1);
      },
      onUnauthorized: () => {
        clearSession();
        setError('Your session has expired. Sign in again to continue.');
      },
    });

  const handleLogout = async () => {
    setLogoutPending(true);
    setError('');
    try {
      await api.logout();
      clearSession();
    } catch (logoutError) {
      if (logoutError instanceof ApiError && logoutError.status === 401) clearSession();
      else setError(messageFor(logoutError));
    } finally {
      setLogoutPending(false);
    }
  };

  const updateTaskForm = <K extends keyof TaskFormState>(field: K, value: TaskFormState[K]) => {
    setTaskForm((current) => ({ ...current, [field]: value }));
    idempotencyKey.current = newIdempotencyKey();
  };

  const handleReferenceImageSelection = (selectedFiles: FileList | null) => {
    if (!selectedFiles?.length) return;
    const images = [...taskForm.referenceImages, ...Array.from(selectedFiles)];
    const totalBytes = images.reduce((total, image) => total + image.size, 0);
    if (images.length > maxReferenceImageCount) {
      setError(`Attach no more than ${maxReferenceImageCount} reference images.`);
    } else if (images.some((image) => image.size < 1 || image.size > maxReferenceImageBytes)) {
      setError('Each reference image must be smaller than 8 MiB.');
    } else if (totalBytes > maxReferenceImageTotalBytes) {
      setError('Reference images must total no more than 40 MiB.');
    } else {
      updateTaskForm('referenceImages', images);
      setError('');
    }
    if (referenceImagesInput.current) referenceImagesInput.current.value = '';
  };

  const removeReferenceImage = (index: number) => {
    updateTaskForm('referenceImages', taskForm.referenceImages.filter((_, imageIndex) => imageIndex !== index));
  };

  const handleCreateTask = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!selectedProjectId) return;
    if (!taskForm.instruction.trim()) {
      setError('Write an instruction before saving the draft.');
      return;
    }
    setTaskPending(true);
    setError('');
    setNotice('');
    try {
      const taskContent = commandToTaskFields(taskForm.instruction);
      await api.createTask(selectedProjectId, {
        assigneeEmployeeId: taskForm.assigneeEmployeeId,
        ...taskContent,
        requiredChecks: [],
        taskType: 'feature',
        priority: taskForm.priority,
      }, idempotencyKey.current, taskForm.referenceImages);
      setTaskForm((current) => ({ ...current, instruction: '', referenceImages: [] }));
      if (referenceImagesInput.current) referenceImagesInput.current.value = '';
      idempotencyKey.current = newIdempotencyKey();
      setNotice('Task saved as DRAFT. It has not been started.');
      setTaskRevision((revision) => revision + 1);
    } catch (createError) {
      setError(messageFor(createError));
    } finally {
      setTaskPending(false);
    }
  };

  const openTaskInspector = (task: Task) => {
    setInspectedTask(task);
    setManifestDigestInput(task.manifestDigest ?? '');
  };

  const commandKeyFor = (task: Task, action: string) => {
    const commandID = `${task.id}:${task.version}:${action}`;
    let commandKey = taskCommandKeys.current.get(commandID);
    if (!commandKey) {
      commandKey = newIdempotencyKey();
      taskCommandKeys.current.set(commandID, commandKey);
    }
    return commandKey;
  };

  const refreshTasksAfterCommandError = (commandError: unknown) => {
    if (commandError instanceof ApiError && (commandError.code === 'stale_task_version' || commandError.code === 'invalid_state_transition' || commandError.code === 'active_attempt_exists')) {
      setTaskRevision((revision) => revision + 1);
    }
  };

  const handleSaveTaskToBacklog = async () => {
    const task = inspectedTask;
    if (!task || task.status !== 'DRAFT') return;
    setTaskCommandPending(true);
    setError('');
    setNotice('');
    try {
      const updatedTask = await api.saveTaskToBacklog(task.projectId, task.id, task.version, commandKeyFor(task, 'BACKLOG'));
      setInspectedTask(null);
      setNotice(`“${updatedTask.title}” saved to BACKLOG. No agent has started.`);
      setTaskRevision((revision) => revision + 1);
    } catch (commandError) {
      setError(describeTaskCommandError(commandError));
      refreshTasksAfterCommandError(commandError);
    } finally {
      setTaskCommandPending(false);
    }
  };

  const handleMarkTaskReady = async () => {
    const task = inspectedTask;
    if (!task || (task.status !== 'DRAFT' && task.status !== 'BACKLOG')) return;
    const digest = manifestDigestInput.trim();
    if (digest && !/^[0-9a-f]{64}$/u.test(digest)) {
      setError('Digest manifest harus berupa 64 karakter heksadesimal (SHA-256).');
      return;
    }
    setTaskCommandPending(true);
    setError('');
    setNotice('');
    try {
      const updatedTask = await api.markTaskReady(task.projectId, task.id, task.version, commandKeyFor(task, 'READY'), digest || undefined);
      setInspectedTask(null);
      setNotice(`“${updatedTask.title}” is READY. Start masih aksi Owner eksplisit.`);
      setTaskRevision((revision) => revision + 1);
    } catch (commandError) {
      setError(describeTaskCommandError(commandError));
      refreshTasksAfterCommandError(commandError);
    } finally {
      setTaskCommandPending(false);
    }
  };

  const handleStartTask = async () => {
    const task = inspectedTask;
    if (!task || task.status !== 'READY') return;
    setTaskCommandPending(true);
    setError('');
    setNotice('');
    try {
      const updatedTask = await api.startTask(task.projectId, task.id, task.version, commandKeyFor(task, 'START'));
      setInspectedTask(null);
      setNotice(`“${updatedTask.title}” entered PROVISIONING. Provisioner akan menyiapkan worktree lalu runtime.`);
      setTaskRevision((revision) => revision + 1);
    } catch (commandError) {
      setError(describeTaskCommandError(commandError));
      refreshTasksAfterCommandError(commandError);
    } finally {
      setTaskCommandPending(false);
    }
  };

  const handleRetryTask = async () => {
    const task = inspectedTask;
    if (!task || (task.status !== 'BLOCKED' && task.status !== 'FAILED')) return;
    setTaskCommandPending(true);
    setError('');
    setNotice('');
    try {
      const updatedTask = await api.retryTask(task.projectId, task.id, task.version, commandKeyFor(task, 'RETRY'));
      setInspectedTask(null);
      setNotice(`“${updatedTask.title}” kembali ke READY. Attempt lama dibatalkan; Start ulang tetap aksi Owner.`);
      setTaskRevision((revision) => revision + 1);
    } catch (commandError) {
      setError(describeTaskCommandError(commandError));
      refreshTasksAfterCommandError(commandError);
    } finally {
      setTaskCommandPending(false);
    }
  };

  const handleCancelTask = async () => {
    const task = inspectedTask;
    if (!task) return;
    setTaskCommandPending(true);
    setError('');
    setNotice('');
    try {
      const updatedTask = await api.cancelTask(task.projectId, task.id, task.version, commandKeyFor(task, 'CANCEL'));
      setInspectedTask(null);
      setNotice(`“${updatedTask.title}” dibatalkan. Sesi agent dan workspace ditutup.`);
      setTaskRevision((revision) => revision + 1);
    } catch (commandError) {
      setError(describeTaskCommandError(commandError));
      refreshTasksAfterCommandError(commandError);
    } finally {
      setTaskCommandPending(false);
    }
  };

  const handleApproveTask = async (headSha: string) => {
    const task = inspectedTask;
    if (!task || task.status !== 'IN_REVIEW') return;
    setTaskCommandPending(true);
    setError('');
    setNotice('');
    try {
      const updatedTask = await api.approveTask(task.projectId, task.id,
        { expectedVersion: task.version, headSha }, commandKeyFor(task, 'APPROVE'));
      setInspectedTask(null);
      setNotice(`“${updatedTask.title}” DONE — SHA ${headSha.slice(0, 8)}… disetujui. Merge PR tetap aksi terpisah di GitHub.`);
      setTaskRevision((revision) => revision + 1);
    } catch (commandError) {
      setError(describeTaskCommandError(commandError));
      refreshTasksAfterCommandError(commandError);
    } finally {
      setTaskCommandPending(false);
    }
  };

  const handleRequestChanges = async (reason: string) => {
    const task = inspectedTask;
    if (!task || task.status !== 'IN_REVIEW') return;
    setTaskCommandPending(true);
    setError('');
    setNotice('');
    try {
      const updatedTask = await api.requestTaskChanges(task.projectId, task.id,
        { expectedVersion: task.version, reason }, commandKeyFor(task, 'REQUEST_CHANGES'));
      setInspectedTask(null);
      setNotice(`“${updatedTask.title}” kembali IN_PROGRESS — Deni merevisi worktree dan PR yang sama.`);
      setTaskRevision((revision) => revision + 1);
    } catch (commandError) {
      setError(describeTaskCommandError(commandError));
      refreshTasksAfterCommandError(commandError);
    } finally {
      setTaskCommandPending(false);
    }
  };

  const handleMergeTask = async () => {
    const task = inspectedTask;
    if (!task || task.status !== 'DONE') return;
    setTaskCommandPending(true);
    setError('');
    setNotice('');
    try {
      await api.mergeTaskPullRequest(task.projectId, task.id,
        { expectedVersion: task.version }, commandKeyFor(task, 'MERGE'));
      const pr = await api.getTaskPullRequest(task.projectId, task.id).catch(() => null);
      setInspectedTaskPR(pr);
      setNotice(pr?.state === 'MERGED'
        ? `PR #${pr.number} tergabung di GitHub — “${task.title}” selesai end-to-end.`
        : `Perintah merge tercatat untuk “${task.title}”.`);
      setTaskRevision((revision) => revision + 1);
    } catch (commandError) {
      setError(describeTaskCommandError(commandError));
      refreshTasksAfterCommandError(commandError);
    } finally {
      setTaskCommandPending(false);
    }
  };

  const handleSaveProvider = async (providerKey: string, input: SaveProviderInput) => {
    if (providerPending) return;
    setProviderPending(providerKey);
    setError('');
    setNotice('');
    try {
      const saved = await api.saveProvider(providerKey, input);
      setProviders((current) => current.map((item) => item.key === providerKey ? saved : item));
      setNotice(saved.enabled
        ? `Provider ${saved.displayName} tersimpan dan aktif.`
        : `Provider ${saved.displayName} tersimpan (nonaktif).`);
    } catch (saveError) {
      setError(messageFor(saveError));
    } finally {
      setProviderPending(null);
    }
  };

  const handleConnectRepository = async (input: { owner: string; name: string; defaultBranch: string }) => {
    if (!selectedProjectId || repositoryPending) return;
    setRepositoryPending(true);
    setError('');
    setNotice('');
    try {
      const connected = await api.saveProjectRepository(selectedProjectId, input);
      setRepository(connected);
      setNotice(`Repository ${connected.owner}/${connected.name} terhubung ke project.`);
    } catch (connectError) {
      setError(messageFor(connectError));
    } finally {
      setRepositoryPending(false);
    }
  };

  const handleOpenProjectFolder = async (editor: FolderEditor) => {
    if (!selectedProjectId || projectFolderStatus !== 'available' || folderPendingEditor) return;
    setFolderPendingEditor(editor);
    setError('');
    setNotice('');
    try {
      await api.openProjectFolder(selectedProjectId, editor);
      setNotice(editor === 'vscode' ? 'Folder project dibuka di VS Code.' : 'Folder project dibuka di File Explorer.');
    } catch (openError) {
      setError(messageFor(openError));
      if (openError instanceof ApiError && openError.code === 'folder_not_configured') {
        setProjectFolderStatus('unavailable');
      } else if (openError instanceof ApiError && openError.code === 'folder_bridge_unavailable') {
        setProjectFolderStatus('error');
      }
    } finally {
      setFolderPendingEditor(null);
    }
  };

  const employeeNames = new Map(employees.map((employee) => [employee.id, employee.name]));
  const canCreateTask = !workspaceLoading && !workspaceLoadError && projects.length > 0 && employees.length > 0;
  const selectedEmployee = employees.find((employee) => employee.id === taskForm.assigneeEmployeeId) ?? employees[0];
  const visibleTasks = taskFilter === 'ALL' ? tasks : tasks.filter((task) => task.status === taskFilter);
  const composeForEmployee = (employee: Employee) => {
    setInspectedEmployee(null);
    setView('compose');
    updateTaskForm('assigneeEmployeeId', employee.id);
    requestAnimationFrame(() => instructionInput.current?.focus());
  };

  return (
    <div className="app-shell pixel-app game-ui" data-session={sessionState}>
      <header className="topbar">
        <a className="brand" href="/" aria-label="DiOffice home">
          <span className="brand-mark" aria-hidden="true"><PixelIcon name="office" /></span>
          <span className="brand-copy">DiOffice<small>Your little software studio</small></span>
        </a>
        {user ? (
          <div className="account-actions">
            <span className="user-label"><span className="user-avatar" aria-hidden="true">{user.displayName.slice(0, 1)}</span>{user.displayName}</span>
            <button className="text-button" type="button" onClick={() => void handleLogout()} disabled={logoutPending || taskPending || taskCommandPending}>
              {logoutPending ? 'Signing out…' : 'Sign out'}
            </button>
          </div>
        ) : <span className="environment-label">OWNER WORKSPACE</span>}
      </header>

      {sessionState === 'checking' ? (
        <main className="loading-page" aria-live="polite">
          <span className="loading-mark" aria-hidden="true"><PixelIcon name="office" /></span>
          <h1>Checking your session</h1>
          <p>Connecting to your DiOffice workspace…</p>
        </main>
      ) : !user ? (
        <main className="auth-page">
          <section className="auth-intro" aria-labelledby="page-title">
            <p className="eyebrow">WELCOME TO YOUR LITTLE SOFTWARE STUDIO</p>
            <h1 id="page-title">Ide kecil.<br />Kerja nyata.</h1>
            <p className="intro">Kantor pixel yang hangat untuk project berikutnya. Beri instruksi, simpan draft, dan tetap pegang kendali.</p>
            <div className="auth-art" aria-hidden="true"><PixelSprite scale={4} /><PixelIcon name="monitor" /><PixelSprite name="plant" scale={3} /><span>YOUR NEXT CHAPTER STARTS HERE</span></div>
            <div className="promise-list">
              <div><PixelIcon name="paper" /><p>Satu instruksi. Satu task yang jelas.</p></div>
              <div><PixelIcon name="team" /><p>Employee persisten, bukan sesi anonim.</p></div>
              <div><PixelIcon name="lock" /><p>Tidak ada pekerjaan yang mulai sendiri.</p></div>
            </div>
          </section>

          <section className="auth-card" aria-labelledby="login-title">
            <div className="card-heading">
              <span className="section-kicker">OWNER SIGN IN</span>
              <h2 id="login-title">Welcome back</h2>
              <p>Sign in to manage your organization’s work.</p>
            </div>
            {error && <p className="notice notice-error" role="alert">{error}</p>}
            <form className="form-stack" onSubmit={(event) => void handleLogin(event)}>
              <label className="field">
                <span>Organization ID</span>
                <input
                  autoComplete="organization"
                  value={loginForm.organizationId}
                  onChange={(event) => setLoginForm((current) => ({ ...current, organizationId: event.target.value }))}
                  placeholder="UUID from Owner bootstrap"
                  required
                />
                <small>Printed once by the local Owner bootstrap command.</small>
              </label>
              <label className="field">
                <span>Email</span>
                <input
                  type="email"
                  autoComplete="username"
                  value={loginForm.email}
                  onChange={(event) => setLoginForm((current) => ({ ...current, email: event.target.value }))}
                  placeholder="you@company.com"
                  required
                />
              </label>
              <label className="field">
                <span>Password</span>
                <input
                  type="password"
                  autoComplete="current-password"
                  value={loginForm.password}
                  onChange={(event) => setLoginForm((current) => ({ ...current, password: event.target.value }))}
                  required
                />
              </label>
              <button className="primary-button" type="submit" disabled={loginPending}>
                {loginPending ? 'Signing in…' : 'Sign in'}
                <span aria-hidden="true">→</span>
              </button>
            </form>
            {devAuthBypass && (
              <div className="dev-auth-panel">
                <p>Local-only development sign-in starts automatically. Use the button only if startup fails.</p>
                <button className="primary-button" type="button" onClick={() => void handleDevelopmentSession()} disabled={loginPending}>
                  {loginPending ? 'Starting local session…' : 'Retry local development sign-in'}
                </button>
              </div>
            )}
            <p className="privacy-note">
              {devAuthBypass
                ? 'This local development session does not use a password.'
                : 'Your session uses secure cookies. Passwords are never saved in this browser.'}
            </p>
          </section>
        </main>
      ) : (
        <div className="workspace-layout" data-view={view}>
        <main className="workspace-page">
          <section className="studio-map-toolbar" aria-label="Studio map">
            <div className="map-window-title"><PixelIcon name="office" /><h1 id="page-title">DiOffice Studio</h1><span>MAIN OFFICE</span></div>
            <div className="studio-mini-map" role="img" aria-label={`World map · ${getStudioArea(ownerWorldX).label} · posisi Owner lokal`}>
              <div className="studio-mini-route">
                <span className="mini-map-zone">OFFICE</span><span className="mini-map-zone">GARDEN</span><span className="mini-map-zone">WORKSHOP</span>
                <span className="mini-map-player" style={{ left: `${ownerWorldX / STUDIO_WORLD_WIDTH * 100}%` }} />
                {selectedEmployee && <span className="mini-map-employee" style={{ left: `${STUDIO_EMPLOYEE_X / STUDIO_WORLD_WIDTH * 100}%` }} />}
              </div>
            </div>
            <label className="project-picker">
              <span>Current project</span>
              <select
                value={selectedProjectId}
                onChange={(event) => setSelectedProjectId(event.target.value)}
                disabled={workspaceLoading || workspaceLoadError || projects.length === 0 || taskPending || taskCommandPending}
              >
                {projects.length === 0 && <option value="">{workspaceLoading ? 'Memuat projects…' : workspaceLoadError ? 'Project belum dimuat' : 'No active projects'}</option>}
                {projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}
              </select>
            </label>
            <ProjectFolderActions
              status={projectFolderStatus}
              pendingEditor={folderPendingEditor}
              onOpen={(editor) => void handleOpenProjectFolder(editor)}
              onRetry={() => setProjectFolderRevision((revision) => revision + 1)}
            />
          </section>

          <div className="studio-notices">
            {view !== 'compose' && error && <p className="notice notice-error workspace-notice" role="alert">{error}</p>}
            {view !== 'compose' && notice && <p className="notice notice-success workspace-notice" role="status">{notice}</p>}
            <EmployeeSnapshotWarning error={employeeSnapshotError} onRetry={() => setEmployeeRevision((current) => current + 1)} />
          </div>

          {workspaceLoadError ? <section className="workspace-unavailable pixel-panel empty-state"><PixelIcon name="refresh" /><h2>Workspace belum bisa dimuat</h2><p>Project dan employee belum diketahui, bukan berarti kantor ini kosong. Muat ulang untuk mengambil state dari server.</p><button type="button" className="secondary-button retry-button" onClick={() => { setError(''); void loadWorkspaceDirectory(); }}>Muat ulang workspace</button></section> : <>
          <div className="office-overview">
            <OfficeScene employee={selectedEmployee} ownerName={user.displayName} loading={workspaceLoading} onInspect={() => selectedEmployee && setInspectedEmployee(selectedEmployee)} onOwnerPositionChange={(x) => setOwnerWorldX(Math.round(x))} />
          </div>

          {view !== 'office' && <InspectorDialog title={view === 'board' ? 'QUEST JOURNAL / TASK BOARD' : view === 'team' ? 'STUDIO TEAM' : view === 'providers' ? 'RUNTIME PROVIDERS' : 'NEW QUEST / DRAFT'} onClose={() => setView('office')}>
          {view === 'team' ? <section className="team-grid" aria-label="Persisted employees">{workspaceLoading ? <p className="panel-hint">Memuat team…</p> : employees.length === 0 ? <p className="empty-message">Belum ada employee persisten.</p> : employees.map((employee) => <article className="team-card pixel-panel" key={employee.id}><div className="window-bar"><span className="window-label"><PixelIcon name="team" />EMPLOYEE</span><StateBadge status={employee.status} /></div><div className="team-card-body"><div className="portrait-frame"><PixelSprite scale={2} /></div><h2>{employee.name}</h2><p>{employee.role}</p><small>{employee.department}</small><button className="secondary-button" type="button" onClick={() => setInspectedEmployee(employee)}><PixelIcon name="monitor" />Open workstation</button><button className="text-button" type="button" onClick={() => composeForEmployee(employee)}>Beri instruksi <span aria-hidden="true">→</span></button></div></article>)}</section> : <>
          <div className="workspace-grid">
            {view === 'compose' && <section className="panel create-panel pixel-panel" aria-labelledby="create-title">
              {error && <p className="notice notice-error" role="alert">{error}</p>}
              {notice && <p className="notice notice-success" role="status">{notice}</p>}
              <div className="window-bar"><span className="window-label"><PixelIcon name="paper" />NEW QUEST</span><span className="scene-floor">DRAFT ONLY</span></div>
              <div className="panel-heading">
                <div className="panel-icon panel-icon-create" aria-hidden="true"><PixelIcon name="plus" /></div>
                <div>
                  <span className="section-kicker">ONE MESSAGE IS ENOUGH</span>
                  <h2 id="create-title">Beri instruksi.</h2>
                </div>
              </div>
              {workspaceLoading ? <p className="panel-hint">Loading your organization…</p> : projects.length === 0 ? (
                <p className="empty-message">No active project is available. Create a project before adding a task.</p>
              ) : employees.length === 0 ? (
                <p className="empty-message">No employee is available to own this task.</p>
              ) : (
                <form className="form-stack" onSubmit={(event) => void handleCreateTask(event)}>
                  <label className="field task-command-field">
                    <span>What should be done?</span>
                    <textarea
                      ref={instructionInput}
                      value={taskForm.instruction}
                      onChange={(event) => updateTaskForm('instruction', event.target.value)}
                      rows={7}
                      maxLength={20000}
                      placeholder="Contoh: Ubah desain halaman task desk agar lebih sederhana dan mengikuti referensi visual ini…"
                      aria-describedby="task-instruction-hint"
                      required
                      disabled={taskPending}
                    />
                    <small id="task-instruction-hint">Write it like a message. The first line becomes the task title automatically; the full instruction is saved.</small>
                  </label>
                  <label className="field image-upload-field">
                    <span>Reference images <span className="optional-label">OPTIONAL</span></span>
                    <input
                      ref={referenceImagesInput}
                      type="file"
                      accept="image/png,image/jpeg"
                      multiple
                      onChange={(event) => handleReferenceImageSelection(event.currentTarget.files)}
                      disabled={taskPending}
                    />
                    <small>PNG or JPEG · up to 5 images · 8 MiB each · stored privately with this draft</small>
                  </label>
                  {taskForm.referenceImages.length > 0 && (
                    <ul className="reference-image-list" aria-label="Selected reference images">
                      {taskForm.referenceImages.map((image, index) => (
                        <li className="reference-image-item" key={`${image.name}-${image.size}-${image.lastModified}-${index}`}>
                          <AttachmentPreview file={image} />
                          <span className="reference-image-name">
                            {image.name}<small>{Math.max(1, Math.ceil(image.size / 1024))} KB</small>
                          </span>
                          <button
                            className="text-button reference-image-remove"
                            type="button"
                            aria-label={`Remove ${image.name}`}
                            onClick={() => removeReferenceImage(index)}
                            disabled={taskPending}
                          >Remove</button>
                        </li>
                      ))}
                    </ul>
                  )}
                  <details className="task-options">
                    <summary>Optional task settings <span>· assignment and priority</span></summary>
                    <div className="field-row">
                      <label className="field">
                        <span>Assign to</span>
                        <select value={taskForm.assigneeEmployeeId} onChange={(event) => updateTaskForm('assigneeEmployeeId', event.target.value)} required disabled={taskPending}>
                          {employees.map((employee) => <option key={employee.id} value={employee.id}>{employee.name} · {employee.role}</option>)}
                        </select>
                      </label>
                      <label className="field">
                        <span>Priority</span>
                        <select value={taskForm.priority} onChange={(event) => updateTaskForm('priority', event.target.value)} disabled={taskPending}>
                          <option value="LOW">Low</option>
                          <option value="NORMAL">Normal</option>
                          <option value="HIGH">High</option>
                          <option value="URGENT">Urgent</option>
                        </select>
                      </label>
                    </div>
                  </details>
                  <button className="primary-button" type="submit" disabled={!canCreateTask || taskPending || !taskForm.instruction.trim()}>
                    {taskPending ? 'Saving draft…' : 'Save draft'}
                    <PixelIcon name="arrow" />
                  </button>
                  <p className="form-footnote">Saving creates a durable draft; it does not start an agent.</p>
                </form>
              )}
            </section>}

            {view === 'board' && <section className="panel tasks-panel pixel-panel" aria-labelledby="tasks-title">
              <div className="window-bar"><span className="window-label"><PixelIcon name="board" />QUEST JOURNAL</span><button className="icon-button" type="button" aria-label="Refresh tasks" disabled={tasksLoading || workspaceLoading} onClick={() => setTaskRevision((revision) => revision + 1)}><PixelIcon name="refresh" /></button></div>
              <div className="tasks-heading">
                <div className="panel-heading">
                  <div className="panel-icon panel-icon-list" aria-hidden="true"><PixelIcon name="board" /></div>
                  <div>
                    <span className="section-kicker">PERSISTED PROJECT TASKS</span>
                    <h2 id="tasks-title">Your quest board</h2>
                  </div>
                </div>
                <span className="task-count">{tasksLoading ? '…' : taskLoadError ? '–' : tasks.length} {tasks.length === 1 ? 'task' : 'tasks'}</span>
              </div>
              <div className="task-filters" role="group" aria-label="Filter tasks by status">{[['ALL', 'All'], ['DRAFT', 'Draft'], ['BACKLOG', 'Backlog'], ['READY', 'Ready'], ['PROVISIONING', 'Provisioning'], ['IN_PROGRESS', 'In progress'], ['WAITING_APPROVAL', 'Approval'], ['BLOCKED', 'Blocked'], ['IN_REVIEW', 'Review'], ['DONE', 'Done'], ['FAILED', 'Failed'], ['CANCELED', 'Canceled']].map(([status, label]) => <button key={status} type="button" aria-pressed={taskFilter === status} onClick={() => setTaskFilter(status)}>{label}</button>)}</div>
              {workspaceLoading ? <div className="empty-state"><span className="loader" aria-hidden="true" /><p>Loading your projects and team…</p></div>
                : projects.length === 0 ? <div className="empty-state"><span className="empty-icon" aria-hidden="true">⌂</span><h3>No project yet</h3><p>Bootstrap creates the initial project. A project-list empty state is shown if none are active.</p></div>
                  : tasksLoading ? <div className="empty-state"><span className="loader" aria-hidden="true" /><p>Loading persisted tasks…</p></div>
                    : taskLoadError ? <div className="empty-state"><span className="empty-icon" aria-hidden="true"><PixelIcon name="refresh" /></span><h3>Task belum bisa dimuat</h3><p>Jumlah dan status task belum diketahui. Refresh untuk mengambil snapshot baru.</p><button type="button" className="secondary-button retry-button" onClick={() => setTaskRevision((revision) => revision + 1)}>Coba lagi</button></div>
                    : visibleTasks.length === 0 ? <div className="empty-state"><span className="empty-icon" aria-hidden="true"><PixelIcon name="paper" /></span><h3>{tasks.length === 0 ? 'Petualangan berikutnya?' : 'Belum ada task di status ini'}</h3><p>{tasks.length === 0 ? 'Tulis instruksi pertama. Task akan muncul di sini setelah draft tersimpan.' : 'Pilih All untuk melihat seluruh task yang dimuat.'}</p></div>
                      : <div className="task-list">
                        {visibleTasks.map((task) => (
                          <article className="task-card" key={task.id}>
                            <div className="task-card-top">
                              <StateBadge status={task.status} />
                              <span className={`priority-label priority-${task.priority.toLowerCase()}`}>{task.priority}</span>
                            </div>
                            <h3><button type="button" className="task-title-button" onClick={() => openTaskInspector(task)} aria-label={`Buka task ${task.title}`}>{task.title}<PixelIcon name="arrow" /></button></h3>
                            <p className="task-description">{task.description}</p>
                            {!!task.referenceImages?.length && (
                              <div className="task-reference-images" aria-label="Task reference images">
                                {task.referenceImages.map((image) => (
                                  <a
                                    key={image.id}
                                    href={api.referenceImageURL(task.projectId, task.id, image.id)}
                                    target="_blank"
                                    rel="noreferrer"
                                  >
                                    <img
                                      src={api.referenceImageURL(task.projectId, task.id, image.id)}
                                      alt={`Reference: ${image.fileName}`}
                                      loading="lazy"
                                      crossOrigin="use-credentials"
                                    />
                                  </a>
                                ))}
                              </div>
                            )}
                            <div className="task-meta">
                              <span className="assignee-avatar" aria-hidden="true">{(employeeNames.get(task.assigneeEmployeeId) ?? '?').slice(0, 1)}</span>
                              <span>{employeeNames.get(task.assigneeEmployeeId) ?? 'Unknown employee'}</span>
                              <time dateTime={task.createdAt}>{formatDate(task.createdAt)}</time>
                            </div>
                          </article>
                        ))}
                      </div>}
              <div className="activity-footnote"><EventConnectionBadge status={activity?.status ?? 'loading'} /><span>Snapshot API · maksimal 100 task · event memicu refresh, bukan mengubah status lokal</span>{activity?.status === 'error' && <button className="text-button" type="button" onClick={retryActivity}>Hubungkan ulang Activity</button>}</div>
            </section>}

            {view === 'providers' && <section className="panel providers-panel pixel-panel" aria-labelledby="providers-title">
              <div className="window-bar"><span className="window-label"><PixelIcon name="monitor" />RUNTIME PROVIDERS</span></div>
              <h2 id="providers-title" className="visually-hidden">Runtime providers</h2>
              <ProvidersPanel providers={providers} pendingKey={providerPending} onSave={(key, input) => void handleSaveProvider(key, input)} />
            </section>}
          </div>
          </>}
          </InspectorDialog>}
          </>}

          <aside className="studio-quest-tracker" aria-label="Quest tracker">
            <div className="map-window-title"><PixelIcon name="board" /><strong>QUEST TRACKER</strong><span>API SNAPSHOT</span></div>
            {tasksLoading || workspaceLoading ? <p>Memuat task tersimpan…</p> : taskLoadError || workspaceLoadError ? <p>Snapshot belum dikonfirmasi. Buka Task board untuk retry.</p> : tasks.length === 0 ? <div className="tracker-empty"><PixelIcon name="paper" /><strong>Belum ada quest</strong><p>Beri instruksi untuk membuat draft pertama.</p><button type="button" onClick={() => setView('compose')} disabled={!canCreateTask}>+ Buat draft</button></div> : <ul>{tasks.slice(0, 3).map((task) => <li key={task.id}><button type="button" onClick={() => openTaskInspector(task)}><span className="tracker-quest-mark" aria-hidden="true">!</span><span><strong>{task.title}</strong><StateBadge status={task.status} /></span></button></li>)}</ul>}
            {tasks.length > 3 && !taskLoadError && <button type="button" className="tracker-all" onClick={() => setView('board')}>Lihat seluruh task yang dimuat ({tasks.length}) <PixelIcon name="arrow" /></button>}
            <p className="tracker-note">Draft ≠ Start · tidak ada kerja simulasi</p>
          </aside>
          <StudioHUD ownerName={user.displayName} employee={selectedEmployee} taskCount={tasksLoading || taskLoadError || workspaceLoading || workspaceLoadError ? undefined : tasks.length} employeeCount={workspaceLoading || workspaceLoadError ? undefined : employees.length} activePane={view} connection={activity?.status ?? 'loading'} instruction={taskForm.instruction} onInstructionChange={(instruction) => updateTaskForm('instruction', instruction)} canCompose={canCreateTask && !taskPending} onNavigate={setView} />
        </main>
        </div>
      )}
      {user && inspectedEmployee && <InspectorDialog title={`${inspectedEmployee.name} / WORKSTATION`} onClose={() => setInspectedEmployee(null)}><WorkstationContent employee={inspectedEmployee} tasks={tasks} activity={activity} employeeSnapshotError={employeeSnapshotError} onRetryEmployee={() => setEmployeeRevision((current) => current + 1)} onRetryActivity={retryActivity} onCompose={() => composeForEmployee(inspectedEmployee)} /></InspectorDialog>}
      {user && inspectedTask && (
        <InspectorDialog title="TASK / QUEST DETAILS" onClose={() => setInspectedTask(null)}>
          <div className="task-detail-heading"><StateBadge status={inspectedTask.status} /><span className="priority-label">{inspectedTask.priority}</span></div>
          <h2 className="task-detail-title">{inspectedTask.title}</h2>
          <p className="task-detail-description">{inspectedTask.description}</p>
          <dl className="task-detail-meta">
            <div><dt>Employee</dt><dd>{employeeNames.get(inspectedTask.assigneeEmployeeId) ?? 'Unknown employee'}</dd></div>
            <div><dt>Saved</dt><dd><time dateTime={inspectedTask.createdAt}>{formatDate(inspectedTask.createdAt)}</time></dd></div>
            <div><dt>Task ID</dt><dd><code>{inspectedTask.id}</code></dd></div>
          </dl>
          {!!inspectedTask.referenceImages?.length && <div className="detail-reference-images">{inspectedTask.referenceImages.map((image) => <a key={image.id} href={api.referenceImageURL(inspectedTask.projectId, inspectedTask.id, image.id)} target="_blank" rel="noreferrer"><img crossOrigin="use-credentials" src={api.referenceImageURL(inspectedTask.projectId, inspectedTask.id, image.id)} alt={image.fileName} /><span>{image.fileName}</span></a>)}</div>}
          <RepositoryConnection repository={repository} pending={repositoryPending} onConnect={(input) => void handleConnectRepository(input)} />
          <PullRequestReview status={inspectedTask.status} pullRequest={inspectedTaskPR} loading={prLoading} pending={taskCommandPending} onApprove={(headSha) => void handleApproveTask(headSha)} onRequestChanges={(reason) => void handleRequestChanges(reason)} onMerge={() => void handleMergeTask()} />
          {(inspectedTask.status === 'DRAFT' || inspectedTask.status === 'BACKLOG') && (
            <ManifestDigestField value={manifestDigestInput} pending={taskCommandPending} onChange={setManifestDigestInput} />
          )}
          <p className="runtime-boundary"><PixelIcon name="lock" />{inspectedTask.status === 'DRAFT'
            ? 'Draft tersimpan. Simpan ke backlog hanya mengantrekan task; agent tidak berjalan.'
            : inspectedTask.status === 'BACKLOG'
              ? 'Task di BACKLOG. Tandai READY setelah repository terhubung dan digest manifest tercatat; Start tetap aksi Owner.'
              : inspectedTask.status === 'READY'
                ? 'READY dikonfirmasi Owner. Start membuat execution attempt baru dan mencatat provisioning.'
                : inspectedTask.status === 'PROVISIONING'
                  ? 'Provisioner menyiapkan worktree terisolasi; session runtime menyusul otomatis.'
                  : inspectedTask.status === 'IN_PROGRESS'
                    ? 'Deni sedang bekerja di worktree. Aktivitas direkam sebagai durable events; cancel menghentikan sesi.'
                    : inspectedTask.status === 'BLOCKED' || inspectedTask.status === 'FAILED'
                      ? 'Eksekusi berhenti dengan reason tercatat di Activity. Retry mengembalikan task ke READY untuk Start ulang eksplisit.'
                      : 'Task detail read-only. Status diverifikasi dari durable events.'}</p>
          <div className="inspector-actions">
            <button type="button" className="secondary-button" onClick={() => setInspectedTask(null)}>Kembali</button>
            <SaveDraftToBacklogButton status={inspectedTask.status} pending={taskCommandPending} onSave={() => void handleSaveTaskToBacklog()} />
            <MarkReadyButton status={inspectedTask.status} pending={taskCommandPending} onMark={() => void handleMarkTaskReady()} />
            <StartTaskButton status={inspectedTask.status} pending={taskCommandPending} onStart={() => void handleStartTask()} />
            <RetryTaskButton status={inspectedTask.status} pending={taskCommandPending} onRetry={() => void handleRetryTask()} />
            <CancelTaskButton status={inspectedTask.status} pending={taskCommandPending} onCancel={() => void handleCancelTask()} />
          </div>
        </InspectorDialog>
      )}
    </div>
  );
}
