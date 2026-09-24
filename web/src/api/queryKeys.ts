/**
 * Central TanStack Query keys. Every key starts with a stable root so events.ts / mutations can
 * invalidate whole resource families (e.g. `queryKeys.duplicates.all` covers lists, stats, detail).
 */
import type {
  ActionListParams,
  DuplicateListParams,
  HistoryListParams,
  Id,
  LogListParams,
  PagingParams,
} from './types';

export const queryKeys = {
  initialize: ['initialize'] as const,
  authStatus: ['auth', 'status'] as const,

  system: {
    all: ['system'] as const,
    status: ['system', 'status'] as const,
    tasks: ['system', 'tasks'] as const,
    backups: ['system', 'backups'] as const,
    logFiles: ['system', 'logFiles'] as const,
    logFile: (filename: string) => ['system', 'logFiles', filename] as const,
    logs: (params: LogListParams = {}) => ['system', 'logs', params] as const,
  },
  health: ['health'] as const,

  commands: {
    all: ['commands'] as const,
    list: ['commands', 'list'] as const,
    detail: (id: Id) => ['commands', 'detail', id] as const,
  },

  config: {
    all: ['config'] as const,
    host: ['config', 'host'] as const,
    settings: ['config', 'settings'] as const,
  },

  mediaServers: {
    all: ['mediaServers'] as const,
    list: ['mediaServers', 'list'] as const,
    detail: (id: Id) => ['mediaServers', 'detail', id] as const,
    libraries: (serverId: Id) => ['mediaServers', 'libraries', serverId] as const,
  },
  libraries: {
    all: ['libraries'] as const,
  },
  plex: {
    all: ['plex'] as const,
    pin: (id: Id) => ['plex', 'pin', id] as const,
    servers: (token: string) => ['plex', 'servers', token] as const,
  },

  arr: {
    all: ['arr'] as const,
    list: ['arr', 'list'] as const,
    detail: (id: Id) => ['arr', 'detail', id] as const,
  },

  tautulli: {
    all: ['tautulli'] as const,
    list: ['tautulli', 'list'] as const,
  },

  pathMappings: {
    all: ['pathMappings'] as const,
    list: ['pathMappings', 'list'] as const,
    detail: (id: Id) => ['pathMappings', 'detail', id] as const,
  },

  profiles: {
    all: ['profiles'] as const,
    list: ['profiles', 'list'] as const,
    detail: (id: Id) => ['profiles', 'detail', id] as const,
    schema: ['profiles', 'schema'] as const,
  },

  duplicates: {
    all: ['duplicates'] as const,
    lists: ['duplicates', 'list'] as const,
    list: (params: DuplicateListParams = {}) => ['duplicates', 'list', params] as const,
    stats: ['duplicates', 'stats'] as const,
    details: ['duplicates', 'detail'] as const,
    detail: (id: Id) => ['duplicates', 'detail', id] as const,
  },

  queue: {
    all: ['queue'] as const,
    list: (params: PagingParams = {}) => ['queue', 'list', params] as const,
  },
  actions: {
    all: ['actions'] as const,
    list: (params: ActionListParams = {}) => ['actions', 'list', params] as const,
  },
  history: {
    all: ['history'] as const,
    list: (params: HistoryListParams = {}) => ['history', 'list', params] as const,
  },
  scans: {
    all: ['scans'] as const,
  },

  exclusions: {
    all: ['exclusions'] as const,
  },

  notifications: {
    all: ['notifications'] as const,
    list: ['notifications', 'list'] as const,
    detail: (id: Id) => ['notifications', 'detail', id] as const,
    schema: ['notifications', 'schema'] as const,
    triggers: ['notifications', 'triggers'] as const,
  },
} as const;
