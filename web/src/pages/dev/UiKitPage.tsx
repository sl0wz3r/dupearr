/**
 * Dev-only UI kit gallery at `/_dev/ui` (not included in production builds). Handy to eyeball
 * components in both themes while building pages.
 */
import { Plus, RefreshCw, Save, Trash } from 'lucide-react';
import { useState } from 'react';
import type { GroupFlag, GroupStatus, SortDirection } from '@/api/types';
import {
  AdvancedSettingsToggle,
  PageBody,
  PageContent,
  PageHeader,
  PageToolbar,
  PageToolbarSection,
  SaveBar,
  SettingsSection,
  ToolbarButton,
  ToolbarSeparator,
  useSettingsForm,
} from '@/components/page';
import {
  ActionStatusBadge,
  AddCard,
  Alert,
  Badge,
  Button,
  ByteSize,
  Card,
  CardGrid,
  Checkbox,
  ConfirmDialog,
  CopyButton,
  DecisionBadge,
  EmptyState,
  FormGroup,
  GroupFlagBadge,
  GroupStatusBadge,
  HealthBadge,
  IconButton,
  Modal,
  NumberInput,
  OrderedListEditor,
  Pagination,
  PasswordInput,
  RelativeTime,
  Select,
  Spinner,
  Switch,
  Table,
  Tabs,
  TextInput,
  Tooltip,
  sortRows,
  useToast,
  type RowId,
  type TableColumn,
} from '@/components/ui';
import { GROUP_STATUSES, RESOLUTION_LABELS, RESOLUTION_ORDER, toOptions } from '@/lib/constants';

interface Row {
  id: number;
  title: string;
  status: GroupStatus;
  size: number;
  seen: string;
}

const ROWS: Row[] = [
  { id: 1, title: 'Blade Runner 2049 (2017)', status: 'pending', size: 71.2e9, seen: new Date(Date.now() - 3e5).toISOString() },
  { id: 2, title: 'Dune (2021)', status: 'review', size: 58.9e9, seen: new Date(Date.now() - 7.2e6).toISOString() },
  { id: 3, title: 'Heat (1995)', status: 'resolved', size: 12.4e9, seen: new Date(Date.now() - 2.6e8).toISOString() },
  { id: 4, title: 'The Office - S02E01 - The Dundies', status: 'ignored', size: 1.2e9, seen: new Date(Date.now() - 9e8).toISOString() },
];

const FLAGS: GroupFlag[] = ['cross_library', 'duration_mismatch', 'multi_episode', 'hardlinked', 'min_age'];

export default function UiKitPage() {
  const toast = useToast();
  const [tab, setTab] = useState<'controls' | 'data' | 'feedback'>('controls');
  const [modal, setModal] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [order, setOrder] = useState<string[]>(['2160', '1080', '720']);
  const [sort, setSort] = useState<{ key: string; dir: SortDirection }>({ key: 'title', dir: 'ascending' });
  const [selected, setSelected] = useState<RowId[]>([]);
  const [page, setPage] = useState(1);
  const form = useSettingsForm({ name: 'Dupearr', port: 3873, secret: '********', enabled: true, level: 'info' });

  const columns: TableColumn<Row>[] = [
    { key: 'title', header: 'Title', sortable: true },
    { key: 'status', header: 'Status', sortable: true, render: (r) => <GroupStatusBadge status={r.status} /> },
    { key: 'size', header: 'Reclaimable', sortable: true, align: 'right', render: (r) => <ByteSize bytes={r.size} /> },
    { key: 'seen', header: 'Last Seen', sortable: true, hideBelow: 'md', render: (r) => <RelativeTime date={r.seen} /> },
  ];

  return (
    <PageContent title="UI Kit">
      <PageToolbar>
        <PageToolbarSection>
          <ToolbarButton icon={RefreshCw} label="Scan Now" spinning onClick={() => toast.info('Scan started')} />
          <ToolbarButton icon={Save} label="Save" loading />
          <ToolbarSeparator />
          <ToolbarButton icon={Trash} label="Delete" kind="danger" onClick={() => setConfirm(true)} />
        </PageToolbarSection>
        <PageToolbarSection align="right">
          <AdvancedSettingsToggle />
        </PageToolbarSection>
      </PageToolbar>
      <PageBody>
        <PageHeader title="UI Kit" subtitle="Development gallery of the Dupearr components" />
        <Tabs
          tabs={[
            { id: 'controls', label: 'Controls' },
            { id: 'data', label: 'Data', badge: ROWS.length },
            { id: 'feedback', label: 'Feedback' },
          ]}
          value={tab}
          onChange={setTab}
          className="mb-5"
        />

        {tab === 'controls' && (
          <>
            <SettingsSection title="Buttons">
              <div className="flex flex-wrap items-center gap-2">
                <Button variant="primary" icon={Plus}>Primary</Button>
                <Button>Default</Button>
                <Button variant="danger" icon={Trash}>Danger</Button>
                <Button variant="success">Success</Button>
                <Button variant="warning">Warning</Button>
                <Button variant="ghost">Ghost</Button>
                <Button variant="primary" loading>Loading</Button>
                <Button size="sm">Small</Button>
                <Button size="lg" variant="primary">Large</Button>
                <IconButton icon={RefreshCw} label="Refresh" />
                <IconButton icon={Trash} label="Delete" variant="danger" />
                <CopyButton value="abc123" />
                <Tooltip content="Decided by resolution: 2160p beats 1080p">
                  <Badge kind="info" outline>Hover me</Badge>
                </Tooltip>
              </div>
            </SettingsSection>

            <SettingsSection title="Form">
              <FormGroup label="Instance Name" htmlFor="kit-name" helpText="Shown in the browser tab">
                <TextInput id="kit-name" value={form.values?.name ?? ''} onChange={(e) => form.setField('name', e.target.value)} />
              </FormGroup>
              <FormGroup label="Port" htmlFor="kit-port" errors={form.values?.port === 80 ? ['Port 80 is reserved'] : null}>
                <NumberInput id="kit-port" value={form.values?.port} min={1} max={65535} onChange={(v) => form.setField('port', v ?? 0)} />
              </FormGroup>
              <FormGroup label="API Key" htmlFor="kit-secret" warning="Changing this breaks connected apps" advanced>
                <PasswordInput id="kit-secret" value={form.values?.secret ?? ''} onChange={(e) => form.setField('secret', e.target.value)} />
              </FormGroup>
              <FormGroup label="Log Level" htmlFor="kit-level">
                <Select
                  id="kit-level"
                  options={[{ value: 'info', label: 'Info' }, { value: 'debug', label: 'Debug' }, { value: 'trace', label: 'Trace' }]}
                  value={form.values?.level ?? ''}
                  onChange={(v) => form.setField('level', v)}
                />
              </FormGroup>
              <FormGroup label="Enabled">
                <Switch checked={form.values?.enabled ?? false} onChange={(v) => form.setField('enabled', v)} label="Enable scanning" description="Runs every 6 hours" />
              </FormGroup>
              <FormGroup label="Remember">
                <Checkbox checked={form.values?.enabled ?? false} onChange={(v) => form.setField('enabled', v)} label="Remember me" />
              </FormGroup>
              <FormGroup label="Resolution Order" helpText="Best first">
                <OrderedListEditor aria-label="Resolution order" value={order} onChange={setOrder} options={toOptions(RESOLUTION_LABELS, RESOLUTION_ORDER)} />
              </FormGroup>
              <SaveBar dirty={form.dirty} onSave={() => form.markSaved(form.values!)} onReset={form.reset} />
            </SettingsSection>
          </>
        )}

        {tab === 'data' && (
          <>
            <SettingsSection title="Table">
              <Table
                columns={columns}
                rows={sortRows(ROWS, sort.key, sort.dir)}
                getRowId={(r) => r.id}
                sortKey={sort.key}
                sortDirection={sort.dir}
                onSortChange={(key, dir) => setSort({ key, dir })}
                selectable
                selectedIds={selected}
                onSelectionChange={setSelected}
              />
              <Pagination page={page} pageSize={20} totalRecords={137} onPageChange={setPage} onPageSizeChange={() => {}} />
            </SettingsSection>
            <SettingsSection title="Badges">
              <div className="flex flex-wrap gap-2">
                {GROUP_STATUSES.map((s) => <GroupStatusBadge key={s} status={s} />)}
              </div>
              <div className="mt-2 flex flex-wrap gap-2">
                {FLAGS.map((f) => <GroupFlagBadge key={f} flag={f} />)}
                <DecisionBadge decision="keep" />
                <DecisionBadge decision="remove" />
                <ActionStatusBadge status="dry_run" />
                <HealthBadge type="warning" />
              </div>
            </SettingsSection>
            <SettingsSection title="Cards">
              <CardGrid>
                <Card title="Plex (Home)" onClick={() => setModal(true)}>
                  <div className="flex gap-1.5"><Badge kind="success">Enabled</Badge><Badge outline>3 libraries</Badge></div>
                </Card>
                <Card title="Radarr 4K" actions={<Badge kind="accent">Radarr</Badge>}>http://radarr:7878</Card>
                <AddCard onClick={() => setModal(true)} label="Add media server" />
              </CardGrid>
            </SettingsSection>
          </>
        )}

        {tab === 'feedback' && (
          <>
            <div className="flex flex-col gap-3">
              <Alert kind="info" title="Info">Dupearr compares copies using your profile.</Alert>
              <Alert kind="warning" title="Dry run">Nothing will be deleted.</Alert>
              <Alert kind="error" title="Connection failed" onDismiss={() => {}}>Unable to connect to Plex.</Alert>
              <Alert kind="success">Backup created.</Alert>
            </div>
            <div className="mt-4 flex flex-wrap items-center gap-3">
              <Spinner size="sm" /> <Spinner /> <Spinner size="lg" />
              <Button onClick={() => setModal(true)}>Open modal</Button>
              <Button onClick={() => toast.success('Saved', 'Settings were saved')}>Toast</Button>
              <Button onClick={() => toast.error('Test failed', 'Unauthorized (check token)')}>Error toast</Button>
            </div>
            <EmptyState title="No duplicates found" description="Run a scan to look for duplicate movies and episodes." action={<Button variant="primary" icon={RefreshCw}>Scan Now</Button>} />
          </>
        )}

        <Modal
          open={modal}
          onClose={() => setModal(false)}
          title="Edit Media Server"
          footerStart={<Button variant="danger">Delete</Button>}
          footer={
            <>
              <Button onClick={() => setModal(false)}>Cancel</Button>
              <Button variant="primary" onClick={() => setModal(false)}>Save</Button>
            </>
          }
        >
          <FormGroup label="Name" htmlFor="m-name"><TextInput id="m-name" defaultValue="Plex" /></FormGroup>
          <FormGroup label="URL" htmlFor="m-url"><TextInput id="m-url" defaultValue="http://192.168.1.10:32400" /></FormGroup>
        </Modal>
        <ConfirmDialog
          open={confirm}
          title="Delete Backup"
          message="Are you sure you want to delete 'dupearr_backup_2026.09.22.zip'?"
          confirmLabel="Delete"
          onCancel={() => setConfirm(false)}
          onConfirm={() => setConfirm(false)}
        />
      </PageBody>
    </PageContent>
  );
}
