import type { CreateTaskInput } from './api';

const maxTaskTitleLength = 200;

type TaskDraftContent = Pick<CreateTaskInput, 'title' | 'description' | 'acceptanceCriteria'>;

export function commandToTaskFields(instruction: string): TaskDraftContent {
  const description = instruction.trim();
  if (!description) throw new Error('Task instruction is required');

  const firstLine = description.split(/\r?\n/u).map((line) => line.trim()).find(Boolean) ?? description;
  const titleCharacters = Array.from(firstLine);
  const title = titleCharacters.length <= maxTaskTitleLength
    ? firstLine
    : `${titleCharacters.slice(0, maxTaskTitleLength - 1).join('')}…`;

  return { title, description, acceptanceCriteria: [] };
}
