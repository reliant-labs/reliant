import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { provisionedDiskGib } from '../daemonUsage';
import { MachinePricingLine, PlanTileRow, commonPlanHours, type ComputePlanOption } from '../PlanTiles';
import { diskGrowPrice } from '@/components/Settings/cloud/diskGrow';
import { formatMachineMinutes, formatMachineMinutesShort } from '@/lib/formatMachineMinutes';

const pricing = { sizes: [{ size: 'small', multiplier: 1, hourlyPriceCents: '13', storageGib: 60 }], storageCentsPerGibMonth: '25', placeholder: false };
const plan: ComputePlanOption = { planId: 'small', label: 'Small', detail: '2 GB · 0.5 CPU', size: 'small', monthlyPriceCents: 1500, includedHours: '160' };

describe('machine pricing disclosure', () => {
  it('reads a machine disk as the one provisioned disk', () => {
    expect(provisionedDiskGib({ capacityGib: 170 })).toBe(170);
    expect(provisionedDiskGib({ capacityGib: 0 })).toBeNull();
    expect(provisionedDiskGib(undefined)).toBeNull();
  });
  it('prices a grow at the most it can add, never more than the GB added', () => {
    expect(diskGrowPrice(pricing, { currentGib: 170, targetGib: 180 })).toEqual({ added: '+$2.50/mo' });
    expect(diskGrowPrice(undefined, { currentGib: 170, targetGib: 180 })).toBeNull();
  });
  it('puts only name, spec and monthly price on a plan row', () => {
    render(<PlanTileRow plan={plan} selected={false} onSelect={() => {}} />);
    const row = screen.getByRole('button');
    expect(row).toHaveTextContent('Small');
    expect(row).toHaveTextContent('2 GB · 0.5 CPU');
    expect(row).toHaveTextContent('$15.00/mo');
    expect(row).not.toHaveTextContent(/hrs|hours|\/hr|GiB|GB\b.*month|suspended|included|shared|overage/i);
  });
  it('claims hours only when every offered plan includes the same own-size hours', () => {
    const at = (includedHours: string | null): ComputePlanOption => ({ ...plan, includedHours });
    expect(commonPlanHours([at('160'), at('160'), at('160')])).toBe('160');
    expect(commonPlanHours([at('160'), at('80')])).toBeNull();
    expect(commonPlanHours([at('160'), at(null)])).toBeNull();
    expect(commonPlanHours([])).toBeNull();
  });
  it('prints one short footnote from the plan hours', () => {
    render(<MachinePricingLine hours="160" />);
    expect(screen.getByTestId('machine-pricing-line')).toHaveTextContent(
      'Includes 160 hours a month. Extra hours and storage are billed separately.',
    );
  });
  it('names coupon time as hours on an M machine (the coupon unit), never jargon', () => {
    expect(formatMachineMinutes(600)).toBe('10 hours on an M machine');
    expect(formatMachineMinutes(60)).toBe('1 hour on an M machine');
    expect(formatMachineMinutesShort(90)).toBe('1.5 h (M)');
  });
});
