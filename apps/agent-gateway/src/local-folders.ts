import { spawn } from 'node:child_process';
import { realpath, stat } from 'node:fs/promises';
import { isAbsolute, resolve } from 'node:path';
import { timingSafeEqual } from 'node:crypto';

export type FolderEditor = 'explorer' | 'vscode';
export type ProjectFolderMap = ReadonlyMap<string, string>;

const projectIDPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/iu;

export function parseProjectFolderMap(raw: string | undefined): ProjectFolderMap {
  if (!raw?.trim()) return new Map();

  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    throw new Error('AGENT_GATEWAY_PROJECT_FOLDERS must be a JSON object');
  }
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error('AGENT_GATEWAY_PROJECT_FOLDERS must be a JSON object');
  }

  const folders = new Map<string, string>();
  for (const [projectID, folder] of Object.entries(value)) {
    if (!projectIDPattern.test(projectID) || typeof folder !== 'string' || !folder.trim() || !isAbsolute(folder)) {
      throw new Error('AGENT_GATEWAY_PROJECT_FOLDERS must map project UUIDs to absolute folder paths');
    }
    folders.set(projectID.toLowerCase(), resolve(folder));
  }
  return folders;
}

export async function resolveProjectFolder(
  projectFolders: ProjectFolderMap,
  projectID: string,
): Promise<string | undefined> {
  if (!projectIDPattern.test(projectID)) return undefined;
  const configuredPath = projectFolders.get(projectID.toLowerCase());
  if (!configuredPath || !isAbsolute(configuredPath)) return undefined;

  try {
    const canonicalPath = await realpath(configuredPath);
    const info = await stat(canonicalPath);
    return info.isDirectory() ? canonicalPath : undefined;
  } catch {
    return undefined;
  }
}

export function hasValidInternalToken(authorization: string | undefined, expectedToken: string | undefined): boolean {
  if (!expectedToken || expectedToken.length < 32 || !authorization?.startsWith('Bearer ')) return false;
  const supplied = Buffer.from(authorization.slice('Bearer '.length));
  const expected = Buffer.from(expectedToken);
  return supplied.length === expected.length && timingSafeEqual(supplied, expected);
}

export async function launchProjectFolder(folderPath: string, editor: FolderEditor): Promise<void> {
  const executable = editor === 'vscode'
    ? 'code'
    : process.platform === 'win32'
      ? 'explorer.exe'
      : process.platform === 'darwin'
        ? 'open'
        : 'xdg-open';
  const child = spawn(executable, [folderPath], {
    detached: true,
    shell: false,
    stdio: 'ignore',
    windowsHide: true,
  });

  await new Promise<void>((resolveSpawn, rejectSpawn) => {
    child.once('error', rejectSpawn);
    child.once('spawn', resolveSpawn);
  });
  child.unref();
}
