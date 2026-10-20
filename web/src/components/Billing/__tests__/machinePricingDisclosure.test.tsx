import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { diskAllowanceGib, provisionedDiskGib } from '../daemonUsage';
import { MachinePricingLine, PlanTileRow, commonPlanHours, type ComputePlanOption } from '../PlanTiles';
import { diskGrowPrice } from '@/components/Settings/cloud/diskGrow';
import { formatMachineMinutes, formatMachineMinutesShort } from '@/lib/formatMachineMinutes';

const pricing = { sizes: [{ size: 'small', multiplier: 1, hourlyPriceCents: '13', storageGib: 120 }], suspendedDiskCentsPerGibMonth: '25', placeholder: false };
const plan: ComputePlanOption = { planId: 'small', label: 'Small', detail: '2 GB · 0.5 CPU', size: 'small', monthlyPriceCents: 1500, includedMinutes: 9600, overageCentsPerMinute: 999, includedHours: '160' };

describe('machine pricing disclosure', () => {
  it('derives the whole grown disk from observed data capacity, not desired or standard capacity', () => {
    expect(diskAllowanceGib(pricing, 'small', 20)).toBe(100);
    expect(provisionedDiskGib(pricing, 'small', { capacityGib: 70, includedGib: 20 })).toBe(170);
    const before = { ...pricing, sizes: [{ ...pricing.sizes[0], storageGib: 20 }] };
    expect(provisionedDiskGib(before, 'small', { capacityGib: 70, includedGib: 20 })).toBe(70);
    expect(provisionedDiskGib(pricing, 'small', { capacityGib: 0, includedGib: 20 })).toBeNull();
    expect(provisionedDiskGib(undefined, 'small', { capacityGib: 70, includedGib: 20 })).toBeNull();
  });
  it('prices whole disks while preserving the same growth delta and running excess', () => {
    expect(diskGrowPrice(pricing, { currentGib: 170, targetGib: 180, includedGib: 120 })).toEqual({ added: '+$2.50/mo', running: '$15.00/mo', runningGib: 60, stopped: '$45.00/mo' });
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
  it('names coupon time as small-machine-hours, not unweighted machine time', () => {
    expect(formatMachineMinutes(600)).toBe('10 small-machine-hours');
    expect(formatMachineMinutesShort(90)).toBe('1.5 small-machine-hours');
  });
});
