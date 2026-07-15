import { slides } from "../slides";
import { SlideLayout } from "../components/SlideLayout";

/** Slide 02: Insight. Thin wrapper — selects its data, renders the layout. */
const data = slides.find((s) => s.id === "insight")!;
export default function Slide02Insight() {
  return <SlideLayout data={data} />;
}
