import { useEffect, useId, useRef, useState, type CSSProperties, type FormEvent, type KeyboardEvent as ReactKeyboardEvent, type MouseEvent as ReactMouseEvent, type ReactNode } from 'react';
import type { Employee, ProviderInfo, PullRequestInfo, Repository, SaveProviderInput, Task } from './api';
import type { EventConnection, ProjectActivity } from './project-events';
import { clampWindowDrag, getStudioArea, getStudioAvatarAnimation, getStudioCameraOffset, getStudioScale, initialStudioAvatar, STUDIO_AREAS, STUDIO_AVATAR_HEIGHT, STUDIO_AVATAR_WIDTH, STUDIO_EMPLOYEE_X, STUDIO_GROUND_Y, STUDIO_OBSTACLES, STUDIO_PLATFORMS, STUDIO_WORKSTATION_X, STUDIO_WORLD_HEIGHT, STUDIO_WORLD_WIDTH, stepStudioAvatar, type StudioArea, type StudioInput } from './studio-navigation';

export type PixelIconName = 'office' | 'board' | 'team' | 'monitor' | 'plus' | 'arrow' | 'leaf' | 'lock' | 'close' | 'paper' | 'refresh' | 'check' | 'alert' | 'link';

const iconPaths: Record<PixelIconName, string> = {
  office: 'M7 1h2v2h2v2h2v2h2v8H1V7h2V5h2V3h2zm-4 8v4h3V9zm7 0v4h3V9z',
  board: 'M2 1h12v14H2zm2 2v2h8V3zm0 4v2h2V7zm4 0v2h4V7zm-4 4v2h2v-2zm4 0v2h4v-2z',
  team: 'M5 1h6v6H5zm-2 8h10v6H3zM1 4h2v4H1zm12 0h2v4h-2z',
  monitor: 'M1 2h14v10H9v2h3v1H4v-1h3v-2H1zm2 2v6h10V4z',
  plus: 'M6 1h4v5h5v4h-5v5H6v-5H1V6h5z',
  arrow: 'M8 2h2v2h2v2h2v4h-2v2h-2v2H8v-4H1V6h7z',
  leaf: 'M8 1h6v6h-2v2h-2v2H8v2H6v2H3v-3h2v-2H3V7h2V5h3z',
  lock: 'M4 1h8v2h2v4h1v8H1V7h1V3h2zm2 2v4h4V3zm1 6v4h2V9z',
  close: 'M2 1h2v2h2v2h4V3h2V1h2v4h-2v2h-2v2h2v2h2v4h-2v-2h-2v-2H6v2H4v2H2v-4h2V9h2V7H4V5H2z',
  paper: 'M3 1h8v2h2v2h1v10H3zm2 6v2h7V7zm0 4v2h5v-2z',
  refresh: 'M5 1h7v2h2v2h1v4H9V7h3V5h-2V3H5v2H3v5h2v2h5v2H4v-2H2v-2H1V5h2V3h2z',
  check: 'M6 10L3 7l-2 2 5 5 9-9-2-2-7 7z',
  alert: 'M8 1l7 14H1L8 1zM7 6v4h2V6H7zm0 5v2h2v-2H7z',
  link: 'M6 1h5v2h3v3h2v5h-2v3h-2v2H9v-2H6v-3H4V6h2V4h2V3H6V1zm2 5H6v3h2v3h3v-2h2V7h-2V6H8z',
};

export function PixelIcon({ name, className = '' }: { name: PixelIconName; className?: string }) {
  return <svg className={`pixel-icon ${className}`} viewBox="0 0 16 16" aria-hidden="true" focusable="false" shapeRendering="crispEdges"><path fill="currentColor" fillRule="evenodd" d={iconPaths[name]} /></svg>;
}

const sprites = {
  deni: [0, 104, 18, 24],
  plant: [168, 64, 20, 20],
  couch: [120, 84, 32, 16],
  bookshelf: [184, 128, 24, 32],
} as const;

export function PixelSprite({ name = 'deni', scale = 3, className = '' }: { name?: keyof typeof sprites; scale?: 2 | 3 | 4; className?: string }) {
  if (name === 'deni') return <img className={`pixel-sprite ${className}`} src="/art/studio-engineer-idle.png" style={{ width: 40 * scale, height: 56 * scale }} alt="" aria-hidden="true" draggable="false" />;
  const [x, y, width, height] = sprites[name];
  const style: CSSProperties = {
    width: width * scale, height: height * scale,
    backgroundImage: 'url(/art/2dpig-office.png)',
    backgroundSize: `${256 * scale}px ${160 * scale}px`,
    backgroundPosition: `${-x * scale}px ${-y * scale}px`,
  };
  return <span className={`pixel-sprite ${className}`} style={style} aria-hidden="true" />;
}

export function StateBadge({ status }: { status: string }) {
  return <span className="state-badge" data-state={status}><span aria-hidden="true" />{status.replaceAll('_', ' ')}</span>;
}

export function SaveDraftToBacklogButton({ status, pending, onSave }: { status: string; pending: boolean; onSave: () => void }) {
  if (status !== 'DRAFT') return null;
  return (
    <button className="primary-button" type="button" onClick={onSave} disabled={pending}>
      {pending ? 'Menyimpan ke backlog…' : 'Simpan ke backlog'}
      <PixelIcon name="arrow" />
    </button>
  );
}

export function MarkReadyButton({ status, pending, onMark }: { status: string; pending: boolean; onMark: () => void }) {
  if (status !== 'DRAFT' && status !== 'BACKLOG') return null;
  return (
    <button className="secondary-button" type="button" onClick={onMark} disabled={pending}>
      {pending ? 'Memeriksa kesiapan…' : 'Tandai READY'}
      <PixelIcon name="arrow" />
    </button>
  );
}

export function StartTaskButton({ status, pending, onStart }: { status: string; pending: boolean; onStart: () => void }) {
  if (status !== 'READY') {
    return (
      <button className="primary-button" type="button" disabled title="Start hanya aktif untuk task READY">
        Start<PixelIcon name="lock" />
      </button>
    );
  }
  return (
    <button className="primary-button" type="button" onClick={onStart} disabled={pending}>
      {pending ? 'Memulai…' : 'Start'}
    </button>
  );
}

const RETRYABLE_STATUSES = new Set(['BLOCKED', 'FAILED']);
const CANCELABLE_STATUSES = new Set(['IN_PROGRESS', 'WAITING_APPROVAL', 'BLOCKED', 'IN_REVIEW', 'FAILED']);

export function RetryTaskButton({ status, pending, onRetry }: { status: string; pending: boolean; onRetry: () => void }) {
  if (!RETRYABLE_STATUSES.has(status)) return null;
  return (
    <button className="secondary-button" type="button" onClick={onRetry} disabled={pending}>
      {pending ? 'Membuka ulang…' : 'Ulangi (ke READY)'}
      <PixelIcon name="arrow" />
    </button>
  );
}

export function CancelTaskButton({ status, pending, onCancel }: { status: string; pending: boolean; onCancel: () => void }) {
  if (!CANCELABLE_STATUSES.has(status)) return null;
  return (
    <button className="danger-button" type="button" onClick={onCancel} disabled={pending}>
      {pending ? 'Membatalkan…' : 'Batalkan task'}
    </button>
  );
}

export function PullRequestReview({
  status,
  pullRequest,
  loading,
  pending,
  onApprove,
  onRequestChanges,
  onMerge,
}: {
  status: string;
  pullRequest: PullRequestInfo | null;
  loading: boolean;
  pending: boolean;
  onApprove: (headSha: string) => void;
  onRequestChanges?: (reason: string) => void;
  onMerge?: () => void;
}) {
  const [changesReason, setChangesReason] = useState('');
  if (status !== 'IN_REVIEW' && status !== 'DONE') return null;
  if (status === 'DONE') {
    const open = pullRequest && (pullRequest.state === 'OPEN' || pullRequest.state === 'DRAFT');
    return (
      <section className="pr-review pixel-panel">
        <p className="runtime-boundary"><PixelIcon name="check" />Task DONE — Owner menyetujui head SHA {pullRequest ? <code>{pullRequest.headSha.slice(0, 8)}…</code> : 'yang tercatat'}.</p>
        {loading ? (
          <p className="empty-message">Memuat evidence pull request…</p>
        ) : pullRequest && open && onMerge ? (
          <>
            <p className="form-footnote">Merge adalah aksi Owner terpisah — DiOffice memverifikasi approval non-stale lalu memanggil GitHub dengan precondition SHA yang disetujui ({pullRequest.headSha.slice(0, 8)}…).</p>
            <button type="button" className="primary-button" disabled={pending} onClick={onMerge}>
              {pending ? 'Menggabungkan…' : `Gabungkan PR #${pullRequest.number} di GitHub`}
            </button>
          </>
        ) : pullRequest && pullRequest.state === 'MERGED' ? (
          <p className="runtime-boundary"><PixelIcon name="check" />PR #{pullRequest.number} sudah tergabung (MERGED){pullRequest.mergedAt ? ` pada ${pullRequest.mergedAt}` : ''}.</p>
        ) : pullRequest ? (
          <p className="runtime-boundary"><PixelIcon name="alert" />PR #{pullRequest.number} berstatus {pullRequest.state} — tidak bisa digabungkan.</p>
        ) : (
          <p className="empty-message">Evidence pull request tidak tercatat.</p>
        )}
      </section>
    );
  }
  if (loading) {
    return <section className="pr-review pixel-panel"><p className="empty-message">Memuat evidence pull request…</p></section>;
  }
  if (!pullRequest) {
    return (
      <section className="pr-review pixel-panel">
        <p className="runtime-boundary"><PixelIcon name="alert" />Task IN_REVIEW tapi evidence pull request tidak ditemukan — approve tidak bisa dilakukan.</p>
      </section>
    );
  }
  return (
    <section className="pr-review pixel-panel">
      <div className="window-bar"><span className="window-label"><PixelIcon name="link" />PULL REQUEST</span><span className="provider-adapter-badge" data-implemented={pullRequest.state === 'OPEN'}>{pullRequest.state}</span></div>
      <dl className="task-detail-meta pr-review-meta">
        <div><dt>PR</dt><dd><a href={pullRequest.url} target="_blank" rel="noreferrer">#{pullRequest.number} — {pullRequest.url}</a></dd></div>
        <div><dt>Branch</dt><dd><code>{pullRequest.branchName}</code></dd></div>
        <div><dt>Head SHA (disetujui)</dt><dd><code>{pullRequest.headSha}</code></dd></div>
        <div><dt>Base SHA</dt><dd><code>{pullRequest.baseSha.slice(0, 12)}…</code></dd></div>
      </dl>
      <p className="form-footnote">Approve mengikat SHA persis ini — head berubah setelah approve membuat approval stale dan task kembali IN_REVIEW. Merge tetap aksi Owner terpisah.</p>
      <button type="button" className="primary-button" disabled={pending} onClick={() => onApprove(pullRequest.headSha)}>
        {pending ? 'Menyetujui…' : `Setujui SHA ${pullRequest.headSha.slice(0, 8)}… → DONE`}
      </button>
      {onRequestChanges && (
        <div className="pr-changes">
          <label className="form-footnote" htmlFor="pr-changes-reason">Minta perubahan — task kembali IN_PROGRESS dan Deni merevisi worktree + PR ini (feedback wajib):</label>
          <textarea id="pr-changes-reason" rows={3} maxLength={2000} disabled={pending}
            value={changesReason} onChange={(event) => setChangesReason(event.target.value)}
            placeholder="Contoh: tambahkan validasi input untuk field email dan perbarui testnya." />
          <button type="button" className="secondary-button" disabled={pending || !changesReason.trim()}
            onClick={() => onRequestChanges(changesReason)}>
            {pending ? 'Mengirim…' : 'Kirim permintaan perubahan → IN_PROGRESS'}
          </button>
        </div>
      )}
    </section>
  );
}

export function ManifestDigestField({ value, pending, onChange }: { value: string; pending: boolean; onChange: (digest: string) => void }) {
  const inputId = useId();
  const fileInput = useRef<HTMLInputElement>(null);
  const digestFromFile = async (file: File) => {
    const bytes = await file.arrayBuffer();
    const digest = await globalThis.crypto.subtle.digest('SHA-256', bytes);
    onChange(Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0')).join(''));
    if (fileInput.current) fileInput.current.value = '';
  };
  return (
    <div className="manifest-digest-field">
      <label htmlFor={inputId}>Digest manifest <code>.dioffice/execution.json</code></label>
      <div className="manifest-digest-controls">
        <input
          id={inputId}
          type="text"
          inputMode="text"
          spellCheck={false}
          placeholder="SHA-256 dari isi file manifest"
          value={value}
          disabled={pending}
          onChange={(event) => onChange(event.target.value.trim().toLowerCase())}
        />
        <input
          ref={fileInput}
          type="file"
          accept=".json,application/json"
          className="visually-hidden"
          aria-label="Hitung digest dari file manifest"
          onChange={(event) => {
            const file = event.target.files?.[0];
            if (file) void digestFromFile(file);
          }}
        />
        <button type="button" className="text-button" disabled={pending} onClick={() => fileInput.current?.click()}>
          Hitung dari file
        </button>
      </div>
      {value && /^[0-9a-f]{64}$/u.test(value) && <p className="form-footnote">Digest tercatat pada task saat READY dikonfirmasi.</p>}
    </div>
  );
}

export function RepositoryConnection({
  repository,
  pending,
  onConnect,
}: {
  repository: Repository | null;
  pending: boolean;
  onConnect: (input: { owner: string; name: string; defaultBranch: string }) => void;
}) {
  const [form, setForm] = useState({ owner: '', name: '', defaultBranch: 'main' });
  if (repository) {
    return (
      <p className="repository-connection">
        <PixelIcon name="monitor" />
        Repository <code>{repository.owner}/{repository.name}</code> · branch <code>{repository.defaultBranch}</code>
      </p>
    );
  }
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    onConnect(form);
  };
  return (
    <form className="repository-connection repository-connection-form" onSubmit={submit}>
      <span className="repository-connection-label"><PixelIcon name="monitor" />Repository belum terhubung</span>
      <input type="text" placeholder="owner" aria-label="GitHub owner" value={form.owner} disabled={pending} onChange={(event) => setForm((current) => ({ ...current, owner: event.target.value }))} />
      <input type="text" placeholder="repo" aria-label="Repository name" value={form.name} disabled={pending} onChange={(event) => setForm((current) => ({ ...current, name: event.target.value }))} />
      <input type="text" placeholder="main" aria-label="Default branch" value={form.defaultBranch} disabled={pending} onChange={(event) => setForm((current) => ({ ...current, defaultBranch: event.target.value }))} />
      <button type="submit" className="secondary-button" disabled={pending || !form.owner.trim() || !form.name.trim() || !form.defaultBranch.trim()}>
        {pending ? 'Menghubungkan…' : 'Hubungkan'}
      </button>
    </form>
  );
}

export type ProjectFolderAvailability = 'checking' | 'available' | 'unavailable' | 'error';

export function ProjectFolderActions({
  status,
  pendingEditor,
  onOpen,
  onRetry,
}: {
  status: ProjectFolderAvailability;
  pendingEditor: 'explorer' | 'vscode' | null;
  onOpen: (editor: 'explorer' | 'vscode') => void;
  onRetry: () => void;
}) {
  return (
    <div className="project-folder-actions" role="group" aria-label="Folder project lokal">
      <span className="project-folder-label">Folder project</span>
      {status === 'checking' && <span role="status">Memeriksa folder lokal…</span>}
      {status === 'error' && <>
        <span role="status">Local folder bridge tidak terhubung</span>
        <button className="text-button" type="button" onClick={onRetry}>Cek ulang folder</button>
      </>}
      {status === 'unavailable' && <>
        <button className="secondary-button" type="button" disabled title="Configure folder allowlist pada local agent gateway">
          Folder lokal belum dikonfigurasi
        </button>
        <button className="text-button" type="button" onClick={onRetry}>Cek ulang folder</button>
      </>}
      {status === 'available' && (
        <>
          <button className="secondary-button" type="button" aria-label="Buka folder project di Explorer" disabled={pendingEditor !== null} onClick={() => onOpen('explorer')}>
            {pendingEditor === 'explorer' ? 'Membuka Explorer…' : 'Explorer'}
          </button>
          <button className="secondary-button" type="button" aria-label="Buka folder project di VS Code" disabled={pendingEditor !== null} onClick={() => onOpen('vscode')}>
            {pendingEditor === 'vscode' ? 'Membuka VS Code…' : 'VS Code'}
          </button>
        </>
      )}
    </div>
  );
}

export function OfficeScene({ employee, ownerName, loading, onInspect, onOwnerPositionChange }: { employee?: Employee; ownerName?: string; loading: boolean; onInspect: () => void; onOwnerPositionChange?: (x: number, area: StudioArea) => void }) {
  const stage = useRef<HTMLDivElement>(null);
  const world = useRef<HTMLDivElement>(null);
  const owner = useRef<HTMLButtonElement>(null);
  const ownerSprite = useRef<HTMLSpanElement>(null);
  const ownerState = useRef(initialStudioAvatar());
  const targetX = useRef<number | null>(null);
  const jumpRequested = useRef(false);
  const reportPosition = useRef(onOwnerPositionChange);
  reportPosition.current = onOwnerPositionChange;
  const [area, setArea] = useState(getStudioArea(ownerState.current.x));
  const currentArea = useRef(area);

  useEffect(() => {
    if (!ownerName) return;
    let animation = 0;
    let previousTime = 0;
    let lastReport = 0;
    let landingUntil = 0;
    let hasMoved = false;
    const frame = (time: number) => {
      const stageElement = stage.current;
      const mapElement = world.current;
      const ownerElement = owner.current;
      if (!stageElement || !mapElement || !ownerElement) return;
      const rect = stageElement.getBoundingClientRect();
      if (rect.height <= 0 || rect.width <= 0) {
        animation = requestAnimationFrame(frame);
        return;
      }
      const scale = getStudioScale(rect.width, rect.height);
      const viewportWidth = rect.width / scale;
      const avatar = ownerState.current;
      const cameraFocus = hasMoved ? avatar.x : Math.max(avatar.x, 270);
      const camera = getStudioCameraOffset(cameraFocus, viewportWidth);
      const verticalOffset = rect.height - STUDIO_WORLD_HEIGHT * scale;
      mapElement.style.transform = `translate3d(${-camera * scale}px, ${verticalOffset}px, 0) scale(${scale})`;
      mapElement.style.setProperty('--world-scale', String(scale));
      mapElement.dataset.cameraX = String(camera);
      const elapsed = previousTime ? Math.min(0.05, (time - previousTime) / 1000) : 0;
      previousTime = time;
      const currentTarget = targetX.current;
      const difference = currentTarget === null ? 0 : currentTarget - avatar.x;
      const direction = Math.abs(difference) < 2 ? 0 : difference < 0 ? -1 : 1;
      const wantsJump = jumpRequested.current;
      if (elapsed > 0) jumpRequested.current = false;
      const input: StudioInput = currentTarget === null
        ? { direction, jump: wantsJump }
        : { direction, jump: wantsJump, targetX: currentTarget };
      const next = stepStudioAvatar(avatar, input, elapsed);
      if (!avatar.grounded && next.grounded) landingUntil = time + 260;
      ownerState.current = next;
      if (currentTarget !== null && (Math.abs(currentTarget - next.x) < 2 || (direction !== 0 && next.x === avatar.x))) {
        targetX.current = null;
      }
      if (next.x !== avatar.x || next.y !== avatar.y) hasMoved = true;
      ownerElement.style.left = `${next.x}px`;
      ownerElement.style.top = `${next.y}px`;
      ownerElement.dataset.position = String(Math.round((next.x / STUDIO_WORLD_WIDTH) * 100));
      ownerElement.dataset.facing = next.facing;
      const walking = direction !== 0 && next.grounded;
      ownerElement.dataset.animation = getStudioAvatarAnimation(next, walking, next.grounded && time < landingUntil);
      ownerElement.dataset.walking = String(walking);
      ownerElement.dataset.jumping = String(!next.grounded);
      const nameplateHalfWidth = Math.min(60, Math.max(0, viewportWidth / 2 - 6));
      const ownerScreenX = next.x - camera;
      const nameplateShift = ownerScreenX < nameplateHalfWidth
        ? nameplateHalfWidth - ownerScreenX
        : ownerScreenX > viewportWidth - nameplateHalfWidth
          ? viewportWidth - nameplateHalfWidth - ownerScreenX
          : 0;
      ownerElement.style.setProperty('--owner-nameplate-shift', `${nameplateShift}px`);
      const nextArea = getStudioArea(next.x);
      const areaChanged = nextArea.id !== currentArea.current.id;
      mapElement.dataset.currentArea = nextArea.id;
      if (areaChanged) {
        currentArea.current = nextArea;
        setArea(nextArea);
      }
      const positionChanged = next.x !== avatar.x || next.y !== avatar.y;
      if (reportPosition.current && positionChanged && (areaChanged || time - lastReport >= 100)) {
        reportPosition.current(next.x, nextArea);
        lastReport = time;
      }
      animation = requestAnimationFrame(frame);
    };
    animation = requestAnimationFrame(frame);
    return () => cancelAnimationFrame(animation);
  }, [ownerName]);

  const focusStage = () => stage.current?.focus({ preventScroll: true });
  const onWorldKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    const target = event.target instanceof Element ? event.target : null;
    const onOwner = Boolean(target?.closest('.world-owner'));
    if (!ownerName || (event.target !== event.currentTarget && !onOwner) || event.altKey || event.ctrlKey || event.metaKey) return;
    if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') {
      event.preventDefault();
      const direction = event.key === 'ArrowLeft' ? -1 : 1;
      const current = ownerState.current;
      targetX.current = Math.max(STUDIO_AVATAR_WIDTH / 2, Math.min(STUDIO_WORLD_WIDTH - STUDIO_AVATAR_WIDTH / 2, current.x + direction * 60));
    } else if (event.key === 'ArrowUp' || event.key === ' ' || event.key === 'Spacebar') {
      event.preventDefault();
      jumpRequested.current = true;
    }
  };
  const onWorldClick = (event: ReactMouseEvent<HTMLDivElement>) => {
    const target = event.target instanceof Element ? event.target : null;
    if (!ownerName || target?.closest('button')) return;
    const rect = stage.current?.getBoundingClientRect();
    if (!rect || rect.height <= 0) return;
    const scale = getStudioScale(rect.width, rect.height);
    const viewportWidth = rect.width / scale;
    const current = ownerState.current;
    const camera = getStudioCameraOffset(hasAvatarMoved(current) ? current.x : Math.max(current.x, 270), viewportWidth);
    targetX.current = Math.max(STUDIO_AVATAR_WIDTH / 2, Math.min(STUDIO_WORLD_WIDTH - STUDIO_AVATAR_WIDTH / 2,
      camera + (event.clientX - rect.left) / scale));
    focusStage();
  };
  const activateJump = () => {
    jumpRequested.current = true;
    focusStage();
  };

  return (
    <section className="office-scene world-scene" data-world="side-view" aria-labelledby="office-title">
      <h2 id="office-title" className="visually-hidden">The studio · world 2D</h2>
      <div ref={stage} className="pixel-stage" role="region" aria-label="World 2D · panah kiri/kanan bergerak · panah atas atau Space melompat" tabIndex={ownerName ? 0 : undefined} onKeyDown={onWorldKeyDown} onClick={onWorldClick}>
        <div ref={world} className="studio-world-map" data-world-width={STUDIO_WORLD_WIDTH} data-current-area={area.id} data-camera-x="0" style={{ width: STUDIO_WORLD_WIDTH, height: STUDIO_WORLD_HEIGHT }}>
          {STUDIO_AREAS.map((zone) => <div key={zone.id} className="studio-world-area" data-area={zone.id} style={{ left: zone.start }}>
            <img className="world-art" src={`/art/studio-world${zone.id === 'main-office' ? '' : `-${zone.id}`}.png`} alt="" aria-hidden="true" draggable="false" />
          </div>)}
          <span className="world-location-sign" style={{ left: 352, top: 154 }}><PixelIcon name="office" /> DiOffice Studio<small>Software guild · main office</small></span>
          <span className="world-zone-sign garden-sign" style={{ left: 960, top: 158 }}>FOREST GARDEN<small>Jelajahi area kebun</small></span>
          <span className="world-zone-sign workshop-sign" style={{ left: 1600, top: 158 }}>WORKSHOP ANNEX<small>Studio kreatif</small></span>
          {STUDIO_PLATFORMS.filter((platform) => platform.y < STUDIO_GROUND_Y).map((platform, index) => <span key={index} className="world-platform" style={{ left: platform.x, top: platform.y, width: platform.width, height: platform.height }} aria-hidden="true" />)}
          {STUDIO_OBSTACLES.map((obstacle) => <span key={obstacle.x} className="world-obstacle" style={{ left: obstacle.x, top: obstacle.y, width: obstacle.width, height: obstacle.height }} aria-hidden="true" />)}
          {ownerName && <button ref={owner} className="world-owner" type="button" style={{ left: ownerState.current.x, top: ownerState.current.y }} data-position={Math.round(ownerState.current.x / STUDIO_WORLD_WIDTH * 100)} data-animation="idle" data-facing="right" data-walking="false" data-jumping="false" aria-label={`Gerakkan avatar ${ownerName}: panah kiri/kanan, panah atas atau Space untuk melompat`} onClick={() => { targetX.current = null; focusStage(); }}>
            <span ref={ownerSprite} className="world-owner-state-sprite" aria-hidden="true" />
            <span className="world-owner-walk-sprite" aria-hidden="true" />
            <img className="world-owner-state-source" src="/art/studio-owner-states.png" alt="" draggable="false" />
            <img className="world-owner-walk-source" src="/art/studio-owner-walk.png" alt="" draggable="false" />
            <span className="employee-nameplate owner-nameplate" title={ownerName}>{ownerName}<small>Owner · lokal</small></span>
          </button>}
          {!loading && employee && <>
            <button className="scene-employee" type="button" style={{ left: STUDIO_EMPLOYEE_X, top: STUDIO_GROUND_Y - STUDIO_AVATAR_HEIGHT - 34 }} onClick={onInspect} aria-label={`Buka profil ${employee.name}`}>
              <span className="employee-bubble"><span>EMPLOYEE</span><StateBadge status={employee.status} /></span>
              <img className="world-character pixel-sprite" src="/art/studio-engineer-idle.png" alt="" draggable="false" />
              <span className="employee-nameplate">{employee.name}<small>{employee.role}</small></span>
            </button>
            <button className="scene-workstation" type="button" style={{ left: STUDIO_WORKSTATION_X, top: STUDIO_GROUND_Y - 110 }} onClick={onInspect} aria-label={`Buka workstation ${employee.name}`}><span className="world-computer" aria-hidden="true"><PixelIcon name="monitor" /></span><span className="object-nameplate">Workstation<small>Klik untuk inspect</small></span></button>
          </>}
          {loading && <div className="scene-message">Memuat kantor…</div>}
          {!loading && !employee && <div className="scene-message">Belum ada employee.<small>Employee persisten akan muncul di sini.</small></div>}
        </div>
        {ownerName && <button className="world-jump" type="button" onClick={activateJump} aria-label="Lompat avatar Owner"><PixelIcon name="arrow" /><span>JUMP</span></button>}
        <div className="scene-footer"><span><strong>{area.label}</strong> · Klik tanah untuk berjalan · ← → bergerak · ↑ / Space lompat · Owner lokal · status employee tetap dari API</span></div>
      </div>
    </section>
  );
}

function hasAvatarMoved(avatar: { x: number; y: number }): boolean {
  const initial = initialStudioAvatar();
  return avatar.x !== initial.x || avatar.y !== initial.y;
}

const workstationTabs = ['Activity', 'Diff', 'Terminal', 'Browser', 'Checks', 'Git / PR', 'Approvals'] as const;

const connectionLabels: Record<EventConnection, string> = { loading: 'Memuat Activity…', connecting: 'Menghubungkan SSE…', live: 'SSE terhubung', reconnecting: 'SSE menghubungkan ulang…', error: 'Activity tidak tersedia', 'signed-out': 'Sesi berakhir' };

export function EventConnectionBadge({ status }: { status: EventConnection }) {
  return <span className="event-connection" data-connection={status} role="status"><span aria-hidden="true" />{connectionLabels[status]}</span>;
}

function WorkstationActivity({ employee, tasks, activity, onRetry }: { employee: Employee; tasks: Task[]; activity?: ProjectActivity; onRetry?: () => void }) {
  const ownedTaskIds = new Set(tasks.filter((task) => task.assigneeEmployeeId === employee.id).map((task) => task.id));
  const events = activity?.items.filter((event) => event.employeeId === employee.id || (event.employeeId === null && event.taskId !== null && ownedTaskIds.has(event.taskId))) ?? [];
  const status = activity?.status ?? 'loading';
  return <section className="workstation-activity" aria-label="Persisted employee activity">
    <div className="activity-heading"><p className="section-kicker">PERSISTED PROJECT EVENTS</p><EventConnectionBadge status={status} /></div>
    {activity?.historyReset && <p className="activity-warning" role="status">Cursor lama tidak dapat direplay. Activity diambil ulang dari snapshot project terbaru; history sebelumnya tidak ditampilkan.</p>}
    {status === 'reconnecting' && <p className="activity-warning">Koneksi terputus. Event yang sudah diterima tetap ditampilkan; replay menunggu koneksi pulih.</p>}
    {(status === 'error' || status === 'signed-out') && <div className="activity-unavailable" role="alert"><strong>{activity?.error ?? 'Activity belum bisa dimuat.'}</strong>{onRetry && status !== 'signed-out' && <button type="button" className="secondary-button retry-button" onClick={onRetry}><PixelIcon name="refresh" />Hubungkan ulang Activity</button>}</div>}
    {events.length > 0 ? <ol className="event-timeline" aria-label="Persisted activity">{[...events].reverse().map((event) => <li key={event.eventId} data-event-id={event.eventId} data-sequence={event.streamSequence}>
      <span className="event-marker" aria-hidden="true"><PixelIcon name="paper" /></span>
      <div><strong>{event.eventType === 'task.created' ? 'Draft tersimpan' : event.eventType === 'task.state_changed' ? 'Status task diperbarui' : event.eventType === 'employee.state_changed' ? 'Status employee diperbarui' : 'Event tersimpan'}</strong>
        {event.eventType === 'task.created' && typeof event.data.title === 'string' && <p className="event-title">{event.data.title.slice(0, 200)}</p>}
        <small><code>{event.eventType}</code><span>#{event.streamSequence}</span><time dateTime={event.recordedAt}>{new Date(event.recordedAt).toLocaleString('id-ID')}</time></small>
      </div>{event.eventType === 'task.created' && event.data.initialState === 'DRAFT' && <StateBadge status="DRAFT" />}
    </li>)}</ol> : !['error', 'signed-out', 'loading', 'connecting'].includes(status) ? <div className="empty-state compact"><PixelIcon name="paper" /><h3>Belum ada event tersimpan untuk {employee.name}</h3><p>Dalam jendela event project yang dimuat. Menyimpan draft menghasilkan event; tidak menjalankan agent.</p></div> : ['loading', 'connecting'].includes(status) ? <p className="panel-hint">Memuat event tersimpan, bukan aktivitas simulasi…</p> : null}
    <p className="activity-window-note">Maksimal 100 event project terbaru · urut sequence{activity?.truncated ? ' · history lebih lama tidak ditampilkan' : ''}. Payload instruksi/terminal mentah tidak ditampilkan di timeline.</p>
  </section>;
}

export function EmployeeSnapshotWarning({ error, onRetry }: { error?: string; onRetry?: () => void }) {
  if (!error) return null;
  return <div className="activity-warning employee-refresh-warning" role="status"><strong>Snapshot employee belum bisa diperbarui.</strong><p>Status terakhir yang berhasil dimuat tetap ditampilkan; belum dikonfirmasi ulang. {error}</p>{onRetry && <button type="button" className="secondary-button" onClick={onRetry}><PixelIcon name="refresh" />Refresh employee</button>}</div>;
}

export function WorkstationContent({ employee, tasks, activity, employeeSnapshotError, onRetryEmployee, onRetryActivity, onCompose }: { employee: Employee; tasks: Task[]; activity?: ProjectActivity; employeeSnapshotError?: string; onRetryEmployee?: () => void; onRetryActivity?: () => void; onCompose: () => void }) {
  const [tab, setTab] = useState<typeof workstationTabs[number]>('Activity');
  const id = useId();

  return <>
    <div className="workstation-identity"><div className="portrait-frame"><PixelSprite scale={2} /></div><div><p className="section-kicker">PERSISTENT EMPLOYEE</p><h2>{employee.name}</h2><p>{employee.role} · {employee.department}</p></div><StateBadge status={employee.status} /></div>
    <EmployeeSnapshotWarning error={employeeSnapshotError} onRetry={onRetryEmployee} />
    <div className="runtime-boundary"><PixelIcon name="monitor" /><div><strong>Belum ada sesi runtime</strong><p>OpenCode belum terhubung. Tidak ada terminal, diff, atau hasil test yang disimulasikan.</p></div></div>
    <div className="workstation-tabs" role="tablist" aria-label="Workstation evidence">
      {workstationTabs.map((name, index) => <button key={name} type="button" role="tab" id={`${id}-tab-${index}`} aria-selected={tab === name} aria-controls={`${id}-panel`} tabIndex={tab === name ? 0 : -1} onClick={() => setTab(name)} onKeyDown={(event) => {
        const next = event.key === 'ArrowRight' ? (index + 1) % workstationTabs.length : event.key === 'ArrowLeft' ? (index + workstationTabs.length - 1) % workstationTabs.length : event.key === 'Home' ? 0 : event.key === 'End' ? workstationTabs.length - 1 : undefined;
        if (next === undefined) return;
        event.preventDefault();
        setTab(workstationTabs[next]);
        document.getElementById(`${id}-tab-${next}`)?.focus();
      }}>{name}</button>)}
    </div>
    <div className="workstation-evidence" id={`${id}-panel`} role="tabpanel" aria-labelledby={`${id}-tab-${workstationTabs.indexOf(tab)}`} tabIndex={0}>
      {tab === 'Activity' ? <WorkstationActivity employee={employee} tasks={tasks} activity={activity} onRetry={onRetryActivity} /> : <div className="empty-state compact"><PixelIcon name="lock" /><h3>{tab} belum tersedia</h3><p>Evidence akan tampil setelah integrasi runtime dan endpoint terkait diimplementasikan. Belum ada data untuk ditampilkan.</p></div>}
    </div>
    <div className="inspector-actions"><button className="secondary-button" type="button" onClick={onCompose}><PixelIcon name="plus" />Beri instruksi</button><button className="primary-button" type="button" disabled title="Explicit Start belum diimplementasikan">Start <PixelIcon name="lock" /></button></div>
  </>;
}

export function InspectorDialog({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const position = useRef({ x: 0, y: 0 });
  const drag = useRef<{ pointerId: number; x: number; y: number; origin: { x: number; y: number }; rect: DOMRect } | null>(null);
  const frame = useRef<number | undefined>(undefined);
  const [moved, setMoved] = useState(false);
  const id = useId();
  const applyPosition = () => {
    frame.current = undefined;
    if (dialog.current) dialog.current.style.transform = `translate(${position.current.x}px, ${position.current.y}px)`;
  };
  const resetPosition = () => {
    if (frame.current !== undefined) cancelAnimationFrame(frame.current);
    drag.current = null;
    position.current = { x: 0, y: 0 };
    applyPosition();
    setMoved(false);
  };
  useEffect(() => {
    const element = dialog.current;
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    element?.showModal();
    window.addEventListener('resize', resetPosition);
    return () => {
      window.removeEventListener('resize', resetPosition);
      if (frame.current !== undefined) cancelAnimationFrame(frame.current);
      element?.close();
      previousFocus?.focus();
    };
  }, []);
  return <dialog ref={dialog} className="inspector-dialog pixel-panel" aria-labelledby={id} onCancel={(event) => { event.preventDefault(); onClose(); }}>
    <div className="window-bar draggable-titlebar" onPointerDown={(event) => {
      if (event.button !== 0 || !dialog.current || (event.target instanceof Element && event.target.closest('button, input, select, textarea, a'))) return;
      drag.current = { pointerId: event.pointerId, x: event.clientX, y: event.clientY, origin: { ...position.current }, rect: dialog.current.getBoundingClientRect() };
      event.currentTarget.setPointerCapture(event.pointerId);
      event.preventDefault();
    }} onPointerMove={(event) => {
      if (!drag.current || drag.current.pointerId !== event.pointerId) return;
      const offset = clampWindowDrag(drag.current.rect, { x: event.clientX - drag.current.x, y: event.clientY - drag.current.y }, { width: window.innerWidth, height: window.innerHeight });
      position.current = { x: drag.current.origin.x + offset.x, y: drag.current.origin.y + offset.y };
      if (frame.current === undefined) frame.current = requestAnimationFrame(applyPosition);
    }} onPointerUp={() => {
      if (!drag.current) return;
      drag.current = null;
      if (frame.current !== undefined) cancelAnimationFrame(frame.current);
      applyPosition();
      setMoved(position.current.x !== 0 || position.current.y !== 0);
    }} onPointerCancel={() => {
      drag.current = null;
      if (frame.current !== undefined) cancelAnimationFrame(frame.current);
      applyPosition();
      setMoved(position.current.x !== 0 || position.current.y !== 0);
    }}><span id={id} className="window-label"><PixelIcon name="monitor" />{title}</span><button className="icon-button reset-window" aria-label="Reset posisi panel" type="button" onClick={resetPosition} disabled={!moved}><PixelIcon name="refresh" /></button><button className="icon-button" aria-label="Tutup panel" type="button" onClick={onClose}><PixelIcon name="close" /></button></div>
    <div className="inspector-body">{children}</div>
  </dialog>;
}

export function AttachmentPreview({ file }: { file: File }) {
  const [url, setURL] = useState('');
  useEffect(() => {
    if (!['image/png', 'image/jpeg'].includes(file.type)) return;
    const objectURL = URL.createObjectURL(file);
    setURL(objectURL);
    return () => URL.revokeObjectURL(objectURL);
  }, [file]);
  return url ? <img className="attachment-preview" src={url} alt={`Preview ${file.name}`} /> : <PixelIcon name="paper" />;
}

function ProviderCard({
  provider,
  pending,
  onSave,
}: {
  provider: ProviderInfo;
  pending: boolean;
  onSave: (input: SaveProviderInput) => void;
}) {
  const fieldId = useId();
  const [form, setForm] = useState<SaveProviderInput>({
    label: provider.label,
    baseUrl: provider.baseUrl ?? '',
    credentialEnv: provider.credentialEnv ?? '',
    enabled: provider.enabled,
  });
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    onSave(form);
  };
  const adapterLabel = provider.adapterStatus === 'implemented'
    ? 'Adapter tersedia'
    : 'Terdaftar — adapter belum diimplementasikan';
  return (
    <article className="provider-card pixel-panel" data-enabled={provider.enabled || undefined}>
      <div className="window-bar">
        <span className="window-label"><PixelIcon name="monitor" />{provider.displayName}</span>
        <span className="provider-adapter-badge" data-implemented={provider.adapterStatus === 'implemented'}>{adapterLabel}</span>
      </div>
      <form className="provider-card-body" onSubmit={submit}>
        <label htmlFor={`${fieldId}-label`}>Label</label>
        <input id={`${fieldId}-label`} type="text" value={form.label} maxLength={80} disabled={pending}
          onChange={(event) => setForm((current) => ({ ...current, label: event.target.value }))} />
        {provider.needsBaseUrl && <>
          <label htmlFor={`${fieldId}-url`}>Endpoint server</label>
          <input id={`${fieldId}-url`} type="url" placeholder="http://127.0.0.1:4096" value={form.baseUrl} disabled={pending}
            onChange={(event) => setForm((current) => ({ ...current, baseUrl: event.target.value }))} />
        </>}
        <label htmlFor={`${fieldId}-cred`}>Nama env credential</label>
        <input id={`${fieldId}-cred`} type="text" placeholder={provider.needsCredential ? 'mis. OPENAI_API_KEY' : 'opsional'} value={form.credentialEnv} disabled={pending}
          onChange={(event) => setForm((current) => ({ ...current, credentialEnv: event.target.value.toUpperCase() }))} />
        <label className="provider-enable" htmlFor={`${fieldId}-enabled`}>
          <input id={`${fieldId}-enabled`} type="checkbox" checked={form.enabled} disabled={pending}
            onChange={(event) => setForm((current) => ({ ...current, enabled: event.target.checked }))} />
          Aktifkan provider ini
        </label>
        <p className="form-footnote">Nilai credential tidak pernah disimpan — hanya nama env var di host runner.</p>
        <button type="submit" className="secondary-button" disabled={pending}>
          {pending ? 'Menyimpan…' : provider.configured ? 'Simpan perubahan' : 'Tambahkan provider'}
        </button>
      </form>
    </article>
  );
}

export function ProvidersPanel({
  providers,
  pendingKey,
  onSave,
}: {
  providers: ProviderInfo[];
  pendingKey: string | null;
  onSave: (providerKey: string, input: SaveProviderInput) => void;
}) {
  if (providers.length === 0) {
    return <p className="empty-message">Katalog provider kosong.</p>;
  }
  return (
    <section className="providers-grid" aria-label="Runtime providers">
      <p className="panel-hint">
        Provider yang aktif dipakai saat attempt dimulai. OpenCode sudah terimplementasi; Codex dan Claude terdaftar tapi adapternya belum ada — task dengan runtime tersebut gagal dengan <code>provider_not_implemented</code>, tidak pernah berpura-pura berjalan.
      </p>
      {providers.map((provider) => (
        <ProviderCard key={provider.key} provider={provider} pending={pendingKey === provider.key}
          onSave={(input) => onSave(provider.key, input)} />
      ))}
    </section>
  );
}
