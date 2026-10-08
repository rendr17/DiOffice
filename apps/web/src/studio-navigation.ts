// Local human navigation only. These coordinates are never employee/API state.
export const STUDIO_WORLD_WIDTH = 1920;
export const STUDIO_WORLD_HEIGHT = 360;
export const STUDIO_AVATAR_WIDTH = 24;
export const STUDIO_AVATAR_HEIGHT = 56;
export const STUDIO_GROUND_Y = 277;
export const STUDIO_INITIAL_X = 100;
export const STUDIO_WORKSTATION_X = 220;
export const STUDIO_EMPLOYEE_X = 360;
export const STUDIO_MAIN_OFFICE_VIEW_WIDTH = 410;

export type StudioArea = { id: 'main-office' | 'garden' | 'workshop'; label: string; start: number; end: number };
export const STUDIO_AREAS: readonly StudioArea[] = [
  { id: 'main-office', label: 'Main Office', start: 0, end: 640 },
  { id: 'garden', label: 'Forest Garden', start: 640, end: 1280 },
  { id: 'workshop', label: 'Workshop Annex', start: 1280, end: STUDIO_WORLD_WIDTH },
];

export type StudioPlatform = { x: number; y: number; width: number; height: number };
export const STUDIO_PLATFORMS: readonly StudioPlatform[] = [
  { x: 0, y: STUDIO_GROUND_Y, width: STUDIO_WORLD_WIDTH, height: 83 },
  { x: 720, y: 228, width: 112, height: 10 },
  { x: 1010, y: 198, width: 128, height: 10 },
  { x: 1450, y: 238, width: 144, height: 10 },
  { x: 1690, y: 204, width: 112, height: 10 },
];

export const STUDIO_OBSTACLES: readonly StudioPlatform[] = [
  { x: 900, y: 241, width: 28, height: 36 },
  { x: 1360, y: 241, width: 32, height: 36 },
];

export type StudioAvatar = { x: number; y: number; velocityY: number; grounded: boolean; facing: 'left' | 'right' };
export type StudioInput = { direction: -1 | 0 | 1; jump?: boolean; targetX?: number };

export function initialStudioAvatar(): StudioAvatar {
  return { x: STUDIO_INITIAL_X, y: STUDIO_GROUND_Y - STUDIO_AVATAR_HEIGHT, velocityY: 0, grounded: true, facing: 'right' };
}

export function getStudioArea(x: number): StudioArea {
  const position = Number.isFinite(x) ? Math.max(0, Math.min(STUDIO_WORLD_WIDTH - 1, x)) : 0;
  return STUDIO_AREAS.find((area) => position >= area.start && position < area.end) ?? STUDIO_AREAS[0];
}

export function getStudioCameraOffset(centerX: number, viewportWorldWidth: number): number {
  const width = Number.isFinite(viewportWorldWidth) ? Math.max(1, Math.min(STUDIO_WORLD_WIDTH, viewportWorldWidth)) : 1;
  const center = Number.isFinite(centerX) ? centerX : width / 2;
  return Math.max(0, Math.min(STUDIO_WORLD_WIDTH - width, center - width / 2));
}

export function getStudioScale(viewportWidth: number, viewportHeight: number): number {
  const width = Number.isFinite(viewportWidth) ? Math.max(1, viewportWidth) : 1;
  const height = Number.isFinite(viewportHeight) ? Math.max(1, viewportHeight) : 1;
  return Math.min(width / STUDIO_MAIN_OFFICE_VIEW_WIDTH, height / STUDIO_WORLD_HEIGHT);
}

export function stepStudioAvatar(
  state: StudioAvatar,
  input: StudioInput,
  elapsedSeconds: number,
  platforms: readonly StudioPlatform[] = STUDIO_PLATFORMS,
  obstacles: readonly StudioPlatform[] = STUDIO_OBSTACLES,
  worldWidth = STUDIO_WORLD_WIDTH,
): StudioAvatar {
  const dt = Number.isFinite(elapsedSeconds) ? Math.max(0, Math.min(0.05, elapsedSeconds)) : 0;
  if (dt === 0) return state;

  const halfWidth = STUDIO_AVATAR_WIDTH / 2;
  let x = Math.max(halfWidth, Math.min(worldWidth - halfWidth, state.x));
  let y = Math.max(0, state.y);
  let velocityY = state.velocityY;
  let grounded = state.grounded;
  const direction = input.direction;
  if (direction !== 0) {
    const speedDistance = 190 * dt;
    const requestedDistance = input.targetX !== undefined && Number.isFinite(input.targetX)
      ? Math.max(0, (input.targetX - x) * direction)
      : speedDistance;
    const nextX = Math.max(halfWidth, Math.min(worldWidth - halfWidth, x + direction * Math.min(speedDistance, requestedDistance)));
    let blocked = false;
    for (const obstacle of obstacles) {
      const verticalOverlap = y < obstacle.y + obstacle.height && y + STUDIO_AVATAR_HEIGHT > obstacle.y;
      if (!verticalOverlap) continue;
      if (direction > 0 && x + halfWidth <= obstacle.x && nextX + halfWidth > obstacle.x) {
        x = obstacle.x - halfWidth;
        blocked = true;
        break;
      } else if (direction < 0 && x - halfWidth >= obstacle.x + obstacle.width && nextX - halfWidth < obstacle.x + obstacle.width) {
        x = obstacle.x + obstacle.width + halfWidth;
        blocked = true;
        break;
      }
    }
    if (!blocked) x = nextX;
  }

  if (input.jump && grounded) {
    velocityY = -390;
    grounded = false;
  }
  if (!grounded) {
    const previousBottom = y + STUDIO_AVATAR_HEIGHT;
    velocityY = Math.min(620, velocityY + 1000 * dt);
    const nextY = Math.max(0, y + velocityY * dt);
    const nextBottom = nextY + STUDIO_AVATAR_HEIGHT;
    if (velocityY >= 0) {
      const landing = platforms
        .filter((platform) => x + halfWidth > platform.x && x - halfWidth < platform.x + platform.width
          && previousBottom <= platform.y && nextBottom >= platform.y)
        .sort((a, b) => a.y - b.y)[0];
      if (landing) {
        y = landing.y - STUDIO_AVATAR_HEIGHT;
        velocityY = 0;
        grounded = true;
      } else y = nextY;
    } else y = nextY;
  }

  return { x, y, velocityY, grounded, facing: direction < 0 ? 'left' : direction > 0 ? 'right' : state.facing };
}

export type StudioAvatarAnimation = 'idle' | 'walk' | 'jump' | 'fall' | 'land';

export function getStudioAvatarAnimation(
  avatar: StudioAvatar,
  walking: boolean,
  landing = false,
): StudioAvatarAnimation {
  if (landing) return 'land';
  if (!avatar.grounded) return avatar.velocityY < 0 ? 'jump' : 'fall';
  return walking ? 'walk' : 'idle';
}

export function clampOwnerPosition(position: number): number {
  return Number.isFinite(position) ? Math.max(16, Math.min(84, position)) : 32;
}

export function advanceOwnerPosition(position: number, direction: 'left' | 'right'): number {
  return clampOwnerPosition(position + (direction === 'left' ? -2 : 2));
}

export function clampWindowDrag(
  rect: { left: number; top: number; width: number; height: number },
  delta: { x: number; y: number },
  viewport: { width: number; height: number },
): { x: number; y: number } {
  return {
    x: Math.max(8 - rect.left, Math.min(viewport.width - rect.width - rect.left - 8, delta.x)),
    y: Math.max(8 - rect.top, Math.min(viewport.height - rect.height - rect.top - 8, delta.y)),
  };
}
