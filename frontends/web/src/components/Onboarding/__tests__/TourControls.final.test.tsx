import {afterEach,expect,it,vi} from 'vitest';
import {cleanup,render,screen} from '@testing-library/react';
import {TourControls} from '../TourControls';
afterEach(cleanup);
it('one-step explanation has no stepper/back/exit and only Done',()=>{
 render(<TourControls stepNumber={1} totalSteps={1} onExit={vi.fn()} onBack={vi.fn()} onNext={vi.fn()} />);
 expect(screen.queryByTestId('tour-step-count')).toBeNull();expect(screen.queryByTestId('tour-back')).toBeNull();expect(screen.queryByTestId('tour-exit')).toBeNull();expect(screen.getAllByRole('button')).toHaveLength(1);expect(screen.getByTestId('tour-next')).toHaveTextContent('Done');
});
it('multi-step final retains Back and progress but not duplicate Dismiss',()=>{
 render(<TourControls stepNumber={3} totalSteps={3} onExit={vi.fn()} onBack={vi.fn()} onNext={vi.fn()} nextLabel="Done" />);
 expect(screen.getByTestId('tour-step-count')).toHaveTextContent('Step 3 of 3');expect(screen.getByTestId('tour-back')).toBeInTheDocument();expect(screen.queryByTestId('tour-exit')).toBeNull();expect(screen.getByTestId('tour-next')).toHaveTextContent('Done');
});
it('earlier steps retain exit, Back, Next and count',()=>{
 render(<TourControls stepNumber={2} totalSteps={3} onExit={vi.fn()} onBack={vi.fn()} onNext={vi.fn()} />);
 expect(screen.getByTestId('tour-step-count')).toHaveTextContent('Step 2 of 3');expect(screen.getByTestId('tour-back')).toBeInTheDocument();expect(screen.getByTestId('tour-exit')).toBeInTheDocument();expect(screen.getByTestId('tour-next')).toHaveTextContent('Next');
});
