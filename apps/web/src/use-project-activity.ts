import { useEffect, useRef, useState } from 'react';
import { ApiClient } from './api';
import { observeProjectEvents, type ProjectActivity } from './project-events';

// A project subscription is disposed on logout/project switch. Event facts do
// not mutate task/employee state: coalesced invalidations refetch API snapshots.
export function useProjectActivity(
  client: ApiClient,
  scope: { organizationId: string; projectId: string } | null,
  callbacks: { onInvalidate: () => void; onUnauthorized: () => void },
) {
  const [state, setState] = useState<ProjectActivity>();
  const [revision, setRevision] = useState(0);
  const currentCallbacks = useRef(callbacks);
  currentCallbacks.current = callbacks;
  const organizationId = scope?.organizationId ?? '';
  const projectId = scope?.projectId ?? '';

  useEffect(() => {
    if (!organizationId || !projectId) return;
    let refreshTimer: ReturnType<typeof setTimeout> | undefined;
    let previousStatus: ProjectActivity['status'] | undefined;
    const invalidate = () => {
      if (refreshTimer !== undefined) return;
      refreshTimer = setTimeout(() => {
        refreshTimer = undefined;
        currentCallbacks.current.onInvalidate();
      }, 200);
    };
    const subscription = observeProjectEvents(client, { organizationId, projectId }, {
      onChange(value) {
        setState(value);
        // Close the race between the initial task snapshot and stream cursor.
        if (value.status === 'live' && previousStatus !== 'live') invalidate();
        previousStatus = value.status;
      },
      onFact(event) {
        if (event.eventType.startsWith('task.') || event.eventType === 'employee.state_changed') invalidate();
      },
      onResync: invalidate,
      onUnauthorized: () => currentCallbacks.current.onUnauthorized(),
    });
    return () => {
      subscription.close();
      if (refreshTimer !== undefined) clearTimeout(refreshTimer);
    };
  }, [client, organizationId, projectId, revision]);

  return {
    activity: scope && state?.projectId === projectId ? state : undefined,
    retry: () => setRevision((current) => current + 1),
  };
}
