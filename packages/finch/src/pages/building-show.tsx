// Building floor view — every room (floor) rendered as its rack rows
// with per-rack U + power meters; click a rack to open its elevation.
// Floors carry their design power budget so draw-vs-design is visible
// at a glance.

import { useState } from 'react';
import { useNavigate, useParams } from 'react-router';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';

import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import ExpandableSection from '@cloudscape-design/components/expandable-section';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import SegmentedControl from '@cloudscape-design/components/segmented-control';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Spinner from '@cloudscape-design/components/spinner';

import { colorTextStatusInactive } from '@cloudscape-design/design-tokens';

import { CapacityBar } from '@/components/capacity-bar';
import { FloorPlan } from '@/components/floor-plan';
import { RackTile, type RackNode } from '@/components/rack-tile';
import { hasCapability } from '@/lib/access-control-provider';
import { http } from '@/lib/http';

type RowNode = { id: string; name: string; code: string; racks: RackNode[] };

type Rollup = {
  u_used: number; u_total: number; u_free: number; u_pct: number;
  kw_max_sum: number | null; kw_current: number | null; kw_pct: number | null;
  racks_total: number; racks_with_kw_rating: number;
};

type FloorNode = {
  id: string; name: string; code: string;
  floor_area_sqft: number | null;
  design_kw: number | null;
  design_cooling_tons: number | null;
  grid_cols: number | null;
  grid_rows: number | null;
  capacity: Rollup;
  rows: RowNode[];
};

type BuildingDetail = {
  building: { id: string; site_id: string; name: string; code: string };
  site: { id: string; name: string; code: string };
  capacity: Rollup;
  floors: FloorNode[];
};

type FloorModalState = { mode: 'create' } | { mode: 'edit'; floor: FloorNode } | null;

export function BuildingShowPage() {
  const { id = '' } = useParams<{ id: string }>();
  const nav = useNavigate();
  const qc = useQueryClient();
  const [floorModal, setFloorModal] = useState<FloorModalState>(null);
  const [deleteFloor, setDeleteFloor] = useState<FloorNode | null>(null);
  const [addRowFloor, setAddRowFloor] = useState<FloorNode | null>(null);

  const detail = useQuery({
    queryKey: ['building-detail', id],
    queryFn: async () => (await http.get<BuildingDetail>(`/dashboards/buildings/${id}`)).data,
    enabled: !!id,
    refetchInterval: 30_000,
  });

  if (detail.isLoading) {
    return (
      <ContentLayout header={<Header variant="h1">Loading…</Header>}>
        <Box textAlign="center" padding="xl"><Spinner size="large" /></Box>
      </ContentLayout>
    );
  }
  if (detail.isError || !detail.data?.building) {
    return (
      <ContentLayout header={<Header variant="h1">Building</Header>}>
        <Box color="text-status-error">Failed to load building.</Box>
      </ContentLayout>
    );
  }

  const { building, site, capacity, floors } = detail.data;
  const canManageFloors = hasCapability('inventory:rooms:create');
  const canEditFloors = hasCapability('inventory:rooms:update');
  const canDeleteFloors = hasCapability('inventory:rooms:delete');
  const canAddRows = hasCapability('inventory:rows:create');

  async function refresh() {
    await qc.invalidateQueries({ queryKey: ['building-detail', id] });
  }

  // Optimistically move the tile, then persist; roll back via
  // invalidate on failure (same posture as rack-visualization).
  function patchPlacement(rackId: string, patch: Partial<Pick<RackNode, 'grid_x' | 'grid_y' | 'grid_rotation'>>) {
    qc.setQueryData<BuildingDetail>(['building-detail', id], (cur) => {
      if (!cur) return cur;
      return {
        ...cur,
        floors: cur.floors.map((f) => ({
          ...f,
          rows: f.rows.map((rw) => ({
            ...rw,
            racks: rw.racks.map((rk) => (rk.id === rackId ? { ...rk, ...patch } : rk)),
          })),
        })),
      };
    });
    http.patch(`/inventory/racks/${rackId}`, patch).catch((err: any) => {
      toast.error(err?.message ?? 'failed to save placement');
      qc.invalidateQueries({ queryKey: ['building-detail', id] });
    });
  }

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          counter={`(${floors.length} floor${floors.length === 1 ? '' : 's'} · ${capacity.racks_total} rack${capacity.racks_total === 1 ? '' : 's'})`}
          description={`Site ${site.code} · ${site.name}`}
          actions={
            <SpaceBetween size="xs" direction="horizontal">
              <Button onClick={() => nav(`/sites/${building.site_id}`)}>Site view</Button>
              <Button onClick={() => nav('/racks/new')}>New rack</Button>
              {canManageFloors && (
                <Button variant="primary" onClick={() => setFloorModal({ mode: 'create' })}>
                  Add floor
                </Button>
              )}
            </SpaceBetween>
          }
        >
          {building.code} · {building.name}
        </Header>
      }
    >
      <SpaceBetween size="l">
        <Container header={<Header variant="h2">Capacity</Header>}>
          <ColumnLayout columns={3} variant="text-grid">
            <div>
              <Box variant="awsui-key-label">Racks</Box>
              <Box variant="h3">{capacity.racks_total}</Box>
              <Box color="text-status-inactive" fontSize="body-s">
                {capacity.racks_with_kw_rating}/{capacity.racks_total} with kW rating
              </Box>
            </div>
            <SpaceBetween size="xs">
              <Box variant="awsui-key-label">Rack space</Box>
              <CapacityBar
                used={capacity.u_used} total={capacity.u_total}
                leftLabel={`${capacity.u_used} / ${capacity.u_total} U`}
              />
              <Box color="text-status-inactive" fontSize="body-s">
                {capacity.u_free} U free
              </Box>
            </SpaceBetween>
            <SpaceBetween size="xs">
              <Box variant="awsui-key-label">Power</Box>
              {capacity.kw_max_sum === null ? (
                <Box color="text-status-inactive">no rack kW ratings configured</Box>
              ) : (
                <>
                  <CapacityBar
                    used={capacity.kw_current ?? 0}
                    total={capacity.kw_max_sum}
                    unknown={capacity.kw_current === null}
                    leftLabel={
                      capacity.kw_current === null
                        ? `— / ${capacity.kw_max_sum.toFixed(0)} kW`
                        : `${capacity.kw_current.toFixed(2)} / ${capacity.kw_max_sum.toFixed(0)} kW`
                    }
                  />
                  <Box color="text-status-inactive" fontSize="body-s">
                    {capacity.kw_current === null
                      ? 'awaiting current PDU telemetry'
                      : `${(capacity.kw_max_sum - capacity.kw_current).toFixed(2)} kW headroom`}
                  </Box>
                </>
              )}
            </SpaceBetween>
          </ColumnLayout>
        </Container>

        {floors.length === 0 && (
          <Container>
            <Box color="text-status-inactive" textAlign="center" padding="m">
              No floors yet. Add a floor, then create rows and racks on it.
            </Box>
          </Container>
        )}
        {floors.map((f) => (
          <FloorSection
            key={f.id}
            floor={f}
            canPlace={hasCapability('inventory:racks:update')}
            onRackClick={(rackId) => nav(`/racks/${rackId}`)}
            onPlace={(rackId, x, y) => patchPlacement(rackId, { grid_x: x, grid_y: y })}
            onRotate={(rackId, rotation) => patchPlacement(rackId, { grid_rotation: rotation })}
            onEdit={canEditFloors ? () => setFloorModal({ mode: 'edit', floor: f }) : undefined}
            onDelete={canDeleteFloors ? () => setDeleteFloor(f) : undefined}
            onAddRow={canAddRows ? () => setAddRowFloor(f) : undefined}
          />
        ))}
      </SpaceBetween>

      {floorModal && (
        <FloorFormModal
          buildingId={id}
          floor={floorModal.mode === 'edit' ? floorModal.floor : null}
          onDismiss={() => setFloorModal(null)}
          onSaved={async () => { setFloorModal(null); await refresh(); }}
        />
      )}
      {deleteFloor && (
        <DeleteFloorModal
          floor={deleteFloor}
          onDismiss={() => setDeleteFloor(null)}
          onDeleted={async () => { setDeleteFloor(null); await refresh(); }}
        />
      )}
      {addRowFloor && (
        <AddRowModal
          floor={addRowFloor}
          onDismiss={() => setAddRowFloor(null)}
          onSaved={async () => { setAddRowFloor(null); await refresh(); }}
        />
      )}
    </ContentLayout>
  );
}

function floorSummary(f: FloorNode): string {
  const parts: string[] = [];
  if (f.floor_area_sqft !== null) parts.push(`${f.floor_area_sqft.toLocaleString()} sqft`);
  if (f.design_kw !== null) parts.push(`${f.design_kw} kW design`);
  if (f.design_cooling_tons !== null) parts.push(`${f.design_cooling_tons} t cooling`);
  return parts.join(' · ');
}

function FloorSection({
  floor, canPlace, onRackClick, onPlace, onRotate, onEdit, onDelete, onAddRow,
}: Readonly<{
  floor: FloorNode;
  canPlace: boolean;
  onRackClick: (rackId: string) => void;
  onPlace: (rackId: string, x: number | null, y: number | null) => void;
  onRotate: (rackId: string, rotation: number) => void;
  onEdit?: () => void;
  onDelete?: () => void;
  onAddRow?: () => void;
}>) {
  const [view, setView] = useState<'plan' | 'rows'>('plan');
  const allRacks = floor.rows.flatMap((rw) => rw.racks);
  const c = floor.capacity;
  // Render the plan as soon as a tile grid is configured, even with no
  // rows yet — editing a floor's grid dims must visibly change the
  // page, not leave it stuck on the empty-state text.
  const hasGrid = floor.grid_cols !== null && floor.grid_rows !== null;
  // Draw vs the floor's design budget when one is set; otherwise fall
  // back to the summed rack ratings.
  const powerTotal = floor.design_kw ?? c.kw_max_sum;
  const powerLabel = floor.design_kw !== null ? 'kW (design)' : 'kW (rack max)';
  return (
    <ExpandableSection
      defaultExpanded
      variant="container"
      headerText={`${floor.code} · ${floor.name}`}
      headerCounter={`(${c.racks_total} rack${c.racks_total === 1 ? '' : 's'})`}
      headerDescription={floorSummary(floor)}
      headerActions={
        (onAddRow || onEdit || onDelete) ? (
          <SpaceBetween size="xs" direction="horizontal">
            {onAddRow && <Button onClick={onAddRow}>Add row</Button>}
            {onEdit && <Button onClick={onEdit}>Edit floor</Button>}
            {onDelete && <Button onClick={onDelete}>Delete</Button>}
          </SpaceBetween>
        ) : undefined
      }
    >
      <SpaceBetween size="m">
        <ColumnLayout columns={2}>
          <CapacityBar
            used={c.u_used} total={c.u_total}
            leftLabel={`${c.u_used} / ${c.u_total} U`}
            rightLabel={`${c.u_free} U free`}
          />
          {powerTotal !== null ? (
            <CapacityBar
              used={c.kw_current ?? 0}
              total={powerTotal}
              unknown={c.kw_current === null}
              leftLabel={
                c.kw_current === null
                  ? `— / ${powerTotal} ${powerLabel}`
                  : `${c.kw_current.toFixed(2)} / ${powerTotal} ${powerLabel}`
              }
              rightLabel={
                c.kw_current === null
                  ? 'awaiting current PDU telemetry'
                  : `${(powerTotal - c.kw_current).toFixed(2)} kW headroom`
              }
            />
          ) : (
            <Box color="text-status-inactive" fontSize="body-s">
              No design kW budget or rack kW ratings on this floor.
            </Box>
          )}
        </ColumnLayout>

        {floor.rows.length > 0 && (
          <SegmentedControl
            selectedId={view}
            onChange={({ detail }) => setView(detail.selectedId as 'plan' | 'rows')}
            label="Floor layout view"
            options={[
              { text: 'Floor plan', id: 'plan' },
              { text: 'Rows', id: 'rows' },
            ]}
          />
        )}
        {(floor.rows.length > 0 || hasGrid) && view === 'plan' && (
          <FloorPlan
            cols={floor.grid_cols}
            rows={floor.grid_rows}
            racks={allRacks}
            canEdit={canPlace}
            onOpenRack={onRackClick}
            onPlace={onPlace}
            onRotate={onRotate}
          />
        )}
        {floor.rows.length === 0 && (
          <SpaceBetween size="xs" direction="horizontal" alignItems="center">
            <Box color="text-status-inactive" fontSize="body-s">
              No rows on this floor yet{onAddRow ? ' — add one, then rack it up.' : '.'}
            </Box>
            {onAddRow && <Button iconName="add-plus" onClick={onAddRow}>Add row</Button>}
          </SpaceBetween>
        )}
        {view === 'rows' && floor.rows.map((rw) => (
          <div key={rw.id} style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <div style={{ fontSize: 12, color: colorTextStatusInactive }}>
              Row <span style={{ fontFamily: 'ui-monospace, monospace' }}>{rw.code}</span> · {rw.name} · {rw.racks.length} rack{rw.racks.length === 1 ? '' : 's'}
            </div>
            {rw.racks.length > 0 ? (
              <div style={{ display: 'flex', gap: 8, overflowX: 'auto', paddingBottom: 4 }}>
                {rw.racks.map((rk) => (
                  <div key={rk.id} style={{ flex: '0 0 230px' }}>
                    <RackTile rack={rk} onClick={() => onRackClick(rk.id)} />
                  </div>
                ))}
              </div>
            ) : (
              <Box color="text-status-inactive" fontSize="body-s">No racks in this row.</Box>
            )}
          </div>
        ))}
      </SpaceBetween>
    </ExpandableSection>
  );
}

function FloorFormModal({
  buildingId, floor, onDismiss, onSaved,
}: Readonly<{
  buildingId: string;
  floor: FloorNode | null;
  onDismiss: () => void;
  onSaved: () => void;
}>) {
  const isEdit = floor !== null;
  const [name, setName] = useState(floor?.name ?? '');
  const [code, setCode] = useState(floor?.code ?? '');
  const [area, setArea] = useState(floor?.floor_area_sqft?.toString() ?? '');
  const [designKw, setDesignKw] = useState(floor?.design_kw?.toString() ?? '');
  const [coolingTons, setCoolingTons] = useState(floor?.design_cooling_tons?.toString() ?? '');
  const [gridCols, setGridCols] = useState(floor?.grid_cols?.toString() ?? '');
  const [gridRows, setGridRows] = useState(floor?.grid_rows?.toString() ?? '');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  async function onSubmit() {
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Name required';
    if (!code.trim()) errs.code = 'Code required';
    if (area && !/^\d+$/.test(area)) errs.area = 'Whole number';
    if (designKw && Number.isNaN(Number(designKw))) errs.designKw = 'Number';
    if (coolingTons && Number.isNaN(Number(coolingTons))) errs.coolingTons = 'Number';
    if (gridCols && (!/^\d+$/.test(gridCols) || Number(gridCols) < 1)) errs.gridCols = 'Whole number ≥ 1';
    if (gridRows && (!/^\d+$/.test(gridRows) || Number(gridRows) < 1)) errs.gridRows = 'Whole number ≥ 1';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    // NUMERIC fields ride as JSON strings on the wire.
    const body = {
      name,
      code,
      floor_area_sqft: area ? Number(area) : null,
      design_kw: designKw || null,
      design_cooling_tons: coolingTons || null,
      grid_cols: gridCols ? Number(gridCols) : null,
      grid_rows: gridRows ? Number(gridRows) : null,
    };
    try {
      if (isEdit) {
        await http.patch(`/inventory/rooms/${floor.id}`, body);
        toast.success('Floor updated');
      } else {
        await http.post('/inventory/rooms', { building_id: buildingId, ...body });
        toast.success('Floor created');
      }
      onSaved();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
      setSubmitting(false);
    }
  }

  return (
    <Modal
      visible
      onDismiss={onDismiss}
      header={isEdit ? `Edit ${floor.code}` : 'Add floor'}
      footer={
        <Box float="right">
          <SpaceBetween size="xs" direction="horizontal">
            <Button onClick={onDismiss}>Cancel</Button>
            <Button variant="primary" loading={submitting} onClick={onSubmit}>
              {isEdit ? 'Save' : 'Create'}
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <SpaceBetween size="m">
        <ColumnLayout columns={2}>
          <FormField label="Name" errorText={errors.name}>
            <Input value={name} onChange={({ detail }) => setName(detail.value)} />
          </FormField>
          <FormField label="Code" errorText={errors.code}>
            <Input value={code} onChange={({ detail }) => setCode(detail.value)} />
          </FormField>
        </ColumnLayout>
        <FormField label="Floor area (sqft)" errorText={errors.area}>
          <Input type="number" value={area} onChange={({ detail }) => setArea(detail.value)} />
        </FormField>
        <ColumnLayout columns={2}>
          <FormField label="Design power (kW)" errorText={errors.designKw}>
            <Input type="number" value={designKw} onChange={({ detail }) => setDesignKw(detail.value)} />
          </FormField>
          <FormField label="Design cooling (tons)" errorText={errors.coolingTons}>
            <Input type="number" value={coolingTons} onChange={({ detail }) => setCoolingTons(detail.value)} />
          </FormField>
        </ColumnLayout>
        <ColumnLayout columns={2}>
          <FormField
            label="Plan grid columns"
            description="Floor-plan tiles across"
            errorText={errors.gridCols}
          >
            <Input type="number" value={gridCols} onChange={({ detail }) => setGridCols(detail.value)} />
          </FormField>
          <FormField
            label="Plan grid rows"
            description="Floor-plan tiles deep"
            errorText={errors.gridRows}
          >
            <Input type="number" value={gridRows} onChange={({ detail }) => setGridRows(detail.value)} />
          </FormField>
        </ColumnLayout>
      </SpaceBetween>
    </Modal>
  );
}

function AddRowModal({
  floor, onDismiss, onSaved,
}: Readonly<{
  floor: FloorNode;
  onDismiss: () => void;
  onSaved: () => void;
}>) {
  const [name, setName] = useState('');
  const [code, setCode] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  async function onSubmit() {
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Name required';
    if (!code.trim()) errs.code = 'Code required';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      await http.post('/inventory/rows', { room_id: floor.id, name, code });
      toast.success('Row created');
      onSaved();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
      setSubmitting(false);
    }
  }

  return (
    <Modal
      visible
      onDismiss={onDismiss}
      header={`Add row to ${floor.code}`}
      footer={
        <Box float="right">
          <SpaceBetween size="xs" direction="horizontal">
            <Button onClick={onDismiss}>Cancel</Button>
            <Button variant="primary" loading={submitting} onClick={onSubmit}>
              Create
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <ColumnLayout columns={2}>
        <FormField label="Name" errorText={errors.name}>
          <Input value={name} onChange={({ detail }) => setName(detail.value)} />
        </FormField>
        <FormField label="Code" errorText={errors.code}>
          <Input value={code} onChange={({ detail }) => setCode(detail.value)} />
        </FormField>
      </ColumnLayout>
    </Modal>
  );
}

function DeleteFloorModal({
  floor, onDismiss, onDeleted,
}: Readonly<{
  floor: FloorNode;
  onDismiss: () => void;
  onDeleted: () => void;
}>) {
  const [submitting, setSubmitting] = useState(false);

  async function onConfirm() {
    setSubmitting(true);
    try {
      await http.delete(`/inventory/rooms/${floor.id}`);
      toast.success('Floor deleted');
      onDeleted();
    } catch (err: any) {
      toast.error(
        err?.response?.status === 409
          ? 'Floor still has rows — delete or move them first.'
          : err?.message ?? 'failed',
      );
      setSubmitting(false);
    }
  }

  return (
    <Modal
      visible
      onDismiss={onDismiss}
      header={`Delete ${floor.code}?`}
      footer={
        <Box float="right">
          <SpaceBetween size="xs" direction="horizontal">
            <Button onClick={onDismiss}>Cancel</Button>
            <Button variant="primary" loading={submitting} onClick={onConfirm}>
              Delete
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <Box>
        This removes floor <b>{floor.code} · {floor.name}</b>. Floors with
        rows can't be deleted until the rows are removed.
      </Box>
    </Modal>
  );
}
