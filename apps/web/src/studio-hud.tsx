import { useEffect, useRef } from 'react';
import type { Employee } from './api';
import type { EventConnection } from './project-events';
import { EventConnectionBadge, PixelIcon, StateBadge, type PixelIconName } from './office-ui';

export type StudioPane = 'office' | 'board' | 'team' | 'compose';

type HUDProps = {
  ownerName: string;
  employee?: Employee;
  taskCount?: number;
  employeeCount?: number;
  activePane: StudioPane;
  connection: EventConnection;
  instruction: string;
  canCompose: boolean;
  onInstructionChange: (instruction: string) => void;
  onNavigate: (pane: StudioPane) => void;
};

const menu: { pane: StudioPane; label: string; icon: PixelIconName; key?: string }[] = [
  { pane: 'office', label: 'Office', icon: 'office' },
  { pane: 'board', label: 'Task board', icon: 'board', key: 'J' },
  { pane: 'team', label: 'Team', icon: 'team', key: 'T' },
  { pane: 'compose', label: 'Beri instruksi', icon: 'paper', key: 'C' },
];

export function StudioHUD({ ownerName, employee, taskCount, employeeCount, activePane, connection, instruction, canCompose, onInstructionChange, onNavigate }: HUDProps) {
  const hud = useRef<HTMLElement>(null);
  useEffect(() => {
    const element = hud.current;
    const root = element?.closest<HTMLElement>('.game-ui');
    if (!element || !root) return;
    const update = (height: number) => {
      const value = `${Math.ceil(height)}px`;
      if (root.style.getPropertyValue('--game-hud-height') !== value) root.style.setProperty('--game-hud-height', value);
    };
    update(element.getBoundingClientRect().height);
    const observer = new ResizeObserver((entries) => {
      const size = entries[0]?.borderBoxSize[0];
      update(size?.blockSize ?? element.getBoundingClientRect().height);
    });
    observer.observe(element);
    return () => { observer.disconnect(); root.style.removeProperty('--game-hud-height'); };
  }, []);
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.altKey || event.ctrlKey || event.metaKey || event.repeat || document.querySelector('dialog[open]')) return;
      const target = event.target;
      if (target instanceof HTMLElement && target.closest('input, textarea, select, button, a, [contenteditable="true"]')) return;
      const item = menu.find((entry) => entry.key?.toLowerCase() === event.key.toLowerCase());
      if (!item || (item.pane === 'compose' && !canCompose)) return;
      event.preventDefault();
      onNavigate(item.pane);
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [canCompose, onNavigate]);

  return <footer ref={hud} className="studio-hud">
    <div className="hud-owner">
      <img src="/art/studio-owner-idle.png" className="hud-portrait" alt="" />
      <div><span className="hud-caption">OWNER CONSOLE</span><strong>{ownerName}</strong><small>DiOffice · software studio</small></div>
    </div>
    <div className="hud-state">
      <div className="hud-state-row"><span>{employee?.name ?? 'Employee'}</span>{employee ? <StateBadge status={employee.status} /> : <span>Belum dimuat</span>}</div>
      <div className="hud-snapshot"><span>{taskCount === undefined ? 'Task belum dikonfirmasi' : `${taskCount} task tersimpan`}</span><span>{employeeCount === undefined ? 'Team belum dikonfirmasi' : `${employeeCount} employee`}</span></div>
      <EventConnectionBadge status={connection} />
    </div>
    <div className="hud-commands">
      <form className="hud-command-bar" onSubmit={(event) => { event.preventDefault(); if (canCompose) onNavigate('compose'); }}>
        <label className="visually-hidden" htmlFor="quick-instruction">Quick instruction</label>
        <span className="hud-command-prefix" aria-hidden="true">TO EMPLOYEE</span>
        <input id="quick-instruction" value={instruction} onChange={(event) => onInstructionChange(event.target.value)} placeholder="Tulis instruksi… Enter untuk review draft" disabled={!canCompose} maxLength={20000} />
        <button type="submit" className="hud-send" aria-label="Review instruksi" disabled={!canCompose}><PixelIcon name="arrow" /></button>
      </form>
      <nav className="studio-nav hud-nav" aria-label="Workspace navigation">
        {menu.map((item) => <button type="button" key={item.pane} aria-current={activePane === item.pane ? 'page' : undefined} onClick={() => onNavigate(item.pane)} disabled={item.pane === 'compose' && !canCompose}><span className="hud-menu-icon"><PixelIcon name={item.icon} /></span><span>{item.label}</span>{item.key && <kbd aria-hidden="true">{item.key}</kbd>}</button>)}
        <button type="button" className="hud-locked" disabled title="Runtime, review, dan approvals belum diimplementasikan"><PixelIcon name="lock" /><span>Runtime</span></button>
      </nav>
    </div>
    <p className="hud-truth">Draft tidak menjalankan agent · snapshot dan koneksi dari API</p>
  </footer>;
}
