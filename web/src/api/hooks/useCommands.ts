/** Commands (docs/API.md → Commands). */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../client';
import { queryKeys } from '../queryKeys';
import type { Command, CommandRequest, Id } from '../types';

/** Recent commands (newest first, 50). Refreshed by the `command` SSE event. */
export function useCommands() {
  return useQuery({
    queryKey: queryKeys.commands.list,
    queryFn: ({ signal }) => api.get<Command[]>('/command', undefined, { signal }),
  });
}

const ACTIVE = new Set(['queued', 'started']);

/** One command; polls every 2s while it is queued/started (SSE also updates it). */
export function useCommand(id: Id | null | undefined) {
  return useQuery({
    queryKey: queryKeys.commands.detail(id ?? 0),
    queryFn: ({ signal }) => api.get<Command>(`/command/${id}`, undefined, { signal }),
    enabled: id != null && id > 0,
    refetchInterval: (query) => (query.state.data && ACTIVE.has(query.state.data.status) ? 2000 : false),
  });
}

/** POST /command — e.g. `run.mutate({ name: 'DuplicateScan' })`. */
export function useRunCommand() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CommandRequest) => api.post<Command>('/command', body),
    onSuccess: (cmd) => {
      qc.setQueryData(queryKeys.commands.detail(cmd.id), cmd);
      void qc.invalidateQueries({ queryKey: queryKeys.commands.list });
      void qc.invalidateQueries({ queryKey: queryKeys.system.tasks });
    },
  });
}

/** True while a command with this name is queued or running (from the recent commands list). */
export function useIsCommandActive(name: Command['name']): boolean {
  const { data } = useCommands();
  return !!data?.some((c) => c.name === name && ACTIVE.has(c.status));
}
