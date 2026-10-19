import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { diskAllowanceGib, provisionedDiskGib, MACHINE_BILLING_POLICY, MACHINE_PRICING_NOTE, sizeFacts } from '../daemonUsage';
import { PlanFinePrint, type ComputePlanOption } from '../PlanTiles';
import { diskGrowPrice } from '@/components/Settings/cloud/diskGrow';
import { formatMachineMinutes, formatMachineMinutesShort } from '@/lib/formatMachineMinutes';

const pricing = { sizes: [{ size: 'small', multiplier: 1, hourlyPriceCents: '13', storageGib: 120 }], suspendedDiskCentsPerGibMonth: '25', placeholder: false };
const plan: ComputePlanOption = { planId: 'small', label: 'Small', size: 'small', monthlyPriceCents: 2900, includedMinutes: 9600, overageCentsPerMinute: 999, includedSmallDaemonHours: 160, facts: sizeFacts(pricing, 'small')! };

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
  it('quotes server hourly pricing, hours only while running, and disk until deletion', () => {
    render(<PlanFinePrint plan={plan} />);
    expect(screen.getByText(/Running machines use included hours/)).toHaveTextContent(MACHINE_BILLING_POLICY);
    expect(screen.getByText(/If your included hours run out/)).toHaveTextContent('$0.13/h past your included hours');
    expect(screen.getByText(/If your included hours run out/)).toHaveTextContent('Overage is off');
    expect(screen.queryByText(/connected|stops the clock|599.40/)).not.toBeInTheDocument();
    expect(MACHINE_PRICING_NOTE).toContain('standard disk is included while running');
  });
  it('names coupon time as small-machine-hours, not unweighted machine time', () => {
    expect(formatMachineMinutes(600)).toBe('10 small-machine-hours');
    expect(formatMachineMinutesShort(90)).toBe('1.5 small-machine-hours');
  });
});
