import test from 'node:test';
import assert from 'node:assert/strict';

test('starts the Owner clear of the workstation nameplate zone', async () => {
  const navigation = await import('./studio-navigation');
  const owner = navigation.initialStudioAvatar();
  assert.ok(navigation.STUDIO_WORKSTATION_X - owner.x >= 100,
    'leave enough world space for the full Owner nameplate and workstation label');
});

test('Owner animation state distinguishes idle, walk, jump, fall, and landing', async () => {
  const navigation = await import('./studio-navigation');
  const getAnimation = Reflect.get(navigation, 'getStudioAvatarAnimation') as (
    avatar: ReturnType<typeof navigation.initialStudioAvatar>, walking: boolean, landing?: boolean,
  ) => string;
  assert.equal(typeof getAnimation, 'function', 'Owner animation state selection is not implemented yet.');
  const idle = navigation.initialStudioAvatar();
  assert.equal(getAnimation(idle, false), 'idle');
  assert.equal(getAnimation(idle, true), 'walk');
  assert.equal(getAnimation({ ...idle, grounded: false, velocityY: -120 }, false), 'jump');
  assert.equal(getAnimation({ ...idle, grounded: false, velocityY: 120 }, false), 'fall');
  assert.equal(getAnimation(idle, false, true), 'land');
});

test('keeps the employee nameplate clear of the workstation label', async () => {
  const navigation = await import('./studio-navigation');
  assert.ok(navigation.STUDIO_EMPLOYEE_X - navigation.STUDIO_WORKSTATION_X >= 90,
    'leave enough world space for the employee and workstation nameplates');
});

test('fits the studio viewport to the main-office world width on narrow screens', async () => {
  const navigation = await import('./studio-navigation');
  assert.equal(typeof navigation.getStudioScale, 'function', 'Responsive world scaling is not implemented yet.');
  const mobileScale = navigation.getStudioScale(390, 622);
  assert.ok(390 / mobileScale >= navigation.STUDIO_MAIN_OFFICE_VIEW_WIDTH);
  assert.ok(622 / mobileScale >= navigation.STUDIO_WORLD_HEIGHT);
  assert.equal(navigation.getStudioScale(1440, 850), 850 / navigation.STUDIO_WORLD_HEIGHT);
});

test('manual Owner movement stays inside the visible world and is never employee state', async () => {
  const navigation = await import('./studio-navigation').catch(() => null);
  assert.ok(navigation, 'Manual local Owner navigation is not implemented yet.');
  assert.ok(navigation.advanceOwnerPosition(50, 'left') < 50);
  assert.ok(navigation.advanceOwnerPosition(50, 'right') > 50);
  assert.equal(navigation.advanceOwnerPosition(16, 'left'), 16);
  assert.equal(navigation.advanceOwnerPosition(84, 'right'), 84);
  assert.equal(navigation.clampOwnerPosition(-100), 16);
  assert.equal(navigation.clampOwnerPosition(100), 84);
  assert.equal(navigation.clampOwnerPosition(Number.NaN), 32);
});

test('explorable world steps the Owner across grounded terrain and clamps the route edges', async () => {
  const navigation = await import('./studio-navigation');
  assert.equal(typeof navigation.stepStudioAvatar, 'function', 'World physics are not implemented yet.');
  const start = navigation.initialStudioAvatar();
  const walked = navigation.stepStudioAvatar(start, { direction: 1 }, 1 / 30);
  assert.ok(walked.x > start.x);
  assert.equal(walked.grounded, true);
  const preciseStop = navigation.stepStudioAvatar(start, { direction: 1, targetX: start.x + 1 }, 0.05);
  assert.equal(preciseStop.x, start.x + 1);
  const right = navigation.stepStudioAvatar({ ...start, x: navigation.STUDIO_WORLD_WIDTH - 12 }, { direction: 1 }, 1 / 30);
  assert.equal(right.x, navigation.STUDIO_WORLD_WIDTH - navigation.STUDIO_AVATAR_WIDTH / 2);
  assert.equal(navigation.getStudioArea(180).id, 'main-office');
  assert.equal(navigation.getStudioArea(800).id, 'garden');
  assert.equal(navigation.getStudioArea(1500).id, 'workshop');
  assert.ok(navigation.getStudioCameraOffset(1000, 640) > 0);
});

test('the Owner can jump, land on a platform, and cannot walk through its solid edge', async () => {
  const navigation = await import('./studio-navigation');
  const start = navigation.initialStudioAvatar();
  const airborne = navigation.stepStudioAvatar(start, { direction: 0, jump: true }, 1 / 30);
  assert.ok(airborne.y < start.y);
  assert.equal(airborne.grounded, false);

  let falling = { ...start, x: 780, y: 150, velocityY: 240, grounded: false };
  for (let frame = 0; frame < 10 && !falling.grounded; frame += 1) {
    falling = navigation.stepStudioAvatar(falling, { direction: 0 }, 1 / 30);
  }
  assert.equal(falling.grounded, true);
  assert.equal(falling.y, 228 - navigation.STUDIO_AVATAR_HEIGHT);

  const wall = navigation.STUDIO_OBSTACLES[0];
  const blocked = navigation.stepStudioAvatar({ ...start, x: wall.x - navigation.STUDIO_AVATAR_WIDTH / 2 - 1, y: 235 },
    { direction: 1 }, 1 / 30);
  assert.equal(blocked.x, wall.x - navigation.STUDIO_AVATAR_WIDTH / 2);
  const remainsBlocked = navigation.stepStudioAvatar(blocked, { direction: 1 }, 1 / 30);
  assert.equal(remainsBlocked.x, blocked.x);
});
