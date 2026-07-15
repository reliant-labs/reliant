import { slides } from "../slides";
import { SlideLayout } from "../components/SlideLayout";

/** Slide 06: Proof. Thin wrapper — selects its data, renders the layout. */
const data = slides.find((s) => s.id === "proof")!;
export default function Slide06Proof() {
  return <SlideLayout data={data} />;
}
