import { slides } from "../slides";
import { SlideLayout } from "../components/SlideLayout";

/** Slide 05: HowItWorks. Thin wrapper — selects its data, renders the layout. */
const data = slides.find((s) => s.id === "how-it-works")!;
export default function Slide05HowItWorks() {
  return <SlideLayout data={data} />;
}
